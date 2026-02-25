package api

import "github.com/CGA1123/krun-api/machine"

// CreateMachineRequest is the JSON body for POST /v1/machines.
type CreateMachineRequest struct {
	Name      string                `json:"name"`
	BaseImage string                `json:"base_image"`
	Config    machine.Config        `json:"config"`
	Network   machine.NetworkConfig `json:"network,omitempty"`
}

// ErrorResponse is a standard error envelope.
type ErrorResponse struct {
	Error string `json:"error"`
}
