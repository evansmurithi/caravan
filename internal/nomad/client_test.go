package nomad

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/nomad/api"
)

// TestHostVolumeRequests checks the HTTP calls the client makes against a fake
// Nomad API.
func TestHostVolumeRequests(t *testing.T) {
	var registered api.HostVolumeRegisterRequest
	var registerNS, listNS string

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/nodes", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]*api.NodeListStub{{ID: "n1", Name: "kamrui"}})
	})
	mux.HandleFunc("GET /v1/volumes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "host" {
			http.Error(w, "unexpected type", http.StatusBadRequest)
			return
		}
		listNS = r.URL.Query().Get("namespace")
		_ = json.NewEncoder(w).Encode([]*api.HostVolumeStub{{ID: "v1", Name: "app-data", NodeID: "n1"}})
	})
	mux.HandleFunc("GET /v1/volume/host/v1", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(&api.HostVolume{ID: "v1", Name: "app-data", HostPath: "/mnt/data1/app"})
	})
	mux.HandleFunc("PUT /v1/volume/host/register", func(w http.ResponseWriter, r *http.Request) {
		registerNS = r.URL.Query().Get("namespace")
		if err := json.NewDecoder(r.Body).Decode(&registered); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(&api.HostVolumeRegisterResponse{Volume: registered.Volume})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c, err := New(Config{Address: srv.URL, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := c.Nodes()
	if err != nil || len(nodes) != 1 || nodes[0].Name != "kamrui" {
		t.Fatalf("Nodes() = %+v, %v", nodes, err)
	}

	stubs, err := c.HostVolumes("apps")
	if err != nil || len(stubs) != 1 || listNS != "apps" {
		t.Fatalf("HostVolumes() = %+v, %v (namespace %q)", stubs, err, listNS)
	}

	vol, err := c.HostVolume("v1", "apps")
	if err != nil || vol.HostPath != "/mnt/data1/app" {
		t.Fatalf("HostVolume() = %+v, %v", vol, err)
	}

	_, err = c.RegisterHostVolume(&api.HostVolume{ID: "v1", Namespace: "apps", Name: "app-data", NodeID: "n1", HostPath: "/mnt/data1/app"})
	if err != nil {
		t.Fatalf("RegisterHostVolume: %v", err)
	}
	if registerNS != "apps" || registered.Volume == nil || registered.Volume.ID != "v1" {
		t.Fatalf("unexpected register request: ns=%q %+v", registerNS, registered.Volume)
	}
}
