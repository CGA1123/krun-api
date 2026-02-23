package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client is a simple HTTP client for the krun-api server.
type Client struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewClient creates a new krun-api client.
func NewClient(baseURL string) *Client {
	return &Client{
		BaseURL:    baseURL,
		HTTPClient: http.DefaultClient,
	}
}

// CreateMachineRequest mirrors the API request body.
type CreateMachineRequest struct {
	Name    string            `json:"name"`
	Config  MachineConfig     `json:"config"`
	Network NetworkConfig     `json:"network,omitempty"`
}

// MachineConfig is the VM configuration sent to the API.
type MachineConfig struct {
	VCPUs      int               `json:"vcpus"`
	MemoryMiB  int               `json:"memory_mib"`
	RootfsPath string            `json:"rootfs_path"`
	ExecPath   string            `json:"exec_path"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Workdir    string            `json:"workdir,omitempty"`
}

// NetworkConfig is the optional network configuration.
type NetworkConfig struct {
	ProxyAddr string   `json:"proxy_addr,omitempty"`
	AllowList []string `json:"allow_list,omitempty"`
}

// MachineResponse is the JSON returned by the API for a machine.
type MachineResponse struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	Config    MachineConfig `json:"config"`
	Network   NetworkConfig `json:"network,omitempty"`
	State     string        `json:"state"`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

// CreateMachine sends POST /v1/machines.
func (c *Client) CreateMachine(req CreateMachineRequest) (*MachineResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	resp, err := c.HTTPClient.Post(c.BaseURL+"/v1/machines", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("POST /v1/machines: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, readAPIError(resp)
	}

	var m MachineResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// StartMachine sends POST /v1/machines/{id}/start.
func (c *Client) StartMachine(id string) (*MachineResponse, error) {
	resp, err := c.HTTPClient.Post(c.BaseURL+"/v1/machines/"+id+"/start", "application/json", nil)
	if err != nil {
		return nil, fmt.Errorf("POST /v1/machines/%s/start: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, readAPIError(resp)
	}

	var m MachineResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, bytes.TrimSpace(body))
}
