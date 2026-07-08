// Package nomad wraps the HashiCorp Nomad HTTP API with the small surface
// caravan needs: parsing HCL, planning diffs, registering and stopping jobs.
package nomad

import (
	"fmt"

	"github.com/hashicorp/nomad/api"
)

// Config configures the Nomad client.
type Config struct {
	Address   string
	Token     string
	Region    string
	Namespace string
}

// Client is a thin wrapper around *api.Client.
type Client struct {
	api       *api.Client
	namespace string
}

// New creates a Nomad API client.
func New(cfg Config) (*Client, error) {
	apiCfg := api.DefaultConfig()
	if cfg.Address != "" {
		apiCfg.Address = cfg.Address
	}
	if cfg.Token != "" {
		apiCfg.SecretID = cfg.Token
	}
	if cfg.Region != "" {
		apiCfg.Region = cfg.Region
	}
	if cfg.Namespace != "" {
		apiCfg.Namespace = cfg.Namespace
	}

	c, err := api.NewClient(apiCfg)
	if err != nil {
		return nil, fmt.Errorf("nomad: creating client: %w", err)
	}
	return &Client{api: c, namespace: cfg.Namespace}, nil
}

// Plan computes the diff between a submitted job and the cluster state.
func (c *Client) Plan(job *api.Job) (*api.JobPlanResponse, error) {
	resp, _, err := c.api.Jobs().Plan(job, true, c.writeOpts(job))
	if err != nil {
		return nil, fmt.Errorf("nomad: planning job %s: %w", jobID(job), err)
	}
	return resp, nil
}

// Register submits a job to the cluster.
func (c *Client) Register(job *api.Job) (*api.JobRegisterResponse, error) {
	resp, _, err := c.api.Jobs().Register(job, c.writeOpts(job))
	if err != nil {
		return nil, fmt.Errorf("nomad: registering job %s: %w", jobID(job), err)
	}
	return resp, nil
}

// Info fetches a job by ID, returning (nil, nil) when the job does not exist.
func (c *Client) Info(jobID, namespace string) (*api.Job, error) {
	job, _, err := c.api.Jobs().Info(jobID, c.queryOpts(namespace))
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("nomad: fetching job %s: %w", jobID, err)
	}
	return job, nil
}

// List returns job stubs in the given namespace ("*" for all namespaces).
func (c *Client) List(namespace string) ([]*api.JobListStub, error) {
	stubs, _, err := c.api.Jobs().List(c.queryOpts(namespace))
	if err != nil {
		return nil, fmt.Errorf("nomad: listing jobs: %w", err)
	}
	return stubs, nil
}

// Stop deregisters a job. When purge is true the job is fully removed from
// Nomad's state rather than being marked dead.
func (c *Client) Stop(jobID, namespace string, purge bool) error {
	_, _, err := c.api.Jobs().Deregister(jobID, purge, c.writeOptsNS(namespace))
	if err != nil {
		return fmt.Errorf("nomad: stopping job %s: %w", jobID, err)
	}
	return nil
}

func (c *Client) writeOpts(job *api.Job) *api.WriteOptions {
	return c.writeOptsNS(namespaceOf(job, c.namespace))
}

func (c *Client) writeOptsNS(namespace string) *api.WriteOptions {
	return &api.WriteOptions{Namespace: namespace}
}

func (c *Client) queryOpts(namespace string) *api.QueryOptions {
	return &api.QueryOptions{Namespace: namespace}
}

func namespaceOf(job *api.Job, fallback string) string {
	if job != nil && job.Namespace != nil && *job.Namespace != "" {
		return *job.Namespace
	}
	return fallback
}

func jobID(job *api.Job) string {
	if job != nil && job.ID != nil {
		return *job.ID
	}
	return "<unknown>"
}

func isNotFound(err error) bool {
	return err != nil && (err.Error() == "job not found" ||
		containsNotFound(err.Error()))
}

func containsNotFound(s string) bool {
	const needle = "not found"
	if len(s) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(s); i++ {
		if s[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
