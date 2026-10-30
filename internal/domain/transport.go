package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type LocalStatus struct {
	Agent           Agent         `json:"agent"`
	Identity        AgentIdentity `json:"identity"`
	Connected       bool          `json:"connected"`
	Ready           bool          `json:"ready"`
	OwnerGeneration int64         `json:"owner_generation,omitempty"`
	Problem         Code          `json:"problem,omitempty"`
}

type ConnectionProfile struct {
	Identity   AgentIdentity `json:"identity"`
	URL        string        `json:"url"`
	CA         string        `json:"ca"`
	SSHHost    string        `json:"ssh_host"`
	SSHPort    int           `json:"ssh_port"`
	KnownHosts string        `json:"known_hosts"`
}

type Health struct {
	APIVersion    int    `json:"api_version"`
	SchemaVersion int    `json:"schema_version"`
	Version       string `json:"version"`
}

type MessageCommand struct {
	Command
	RecipientID     string         `json:"recipient_id"`
	Body            string         `json:"body"`
	Origin          string         `json:"origin"`
	Kind            string         `json:"kind"`
	Urgent          bool           `json:"urgent"`
	ResponseRequest bool           `json:"response_requested"`
	ExpiresAt       *int64         `json:"expires_at,omitempty"`
	ReplyToID       string         `json:"reply_to_id,omitempty"`
	Insistence      bool           `json:"insistence,omitempty"`
	Request         *MessageAction `json:"request,omitempty"`
}

func (command *MessageCommand) UnmarshalJSON(data []byte) error {
	type input MessageCommand
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return Fail(Invalid, "one message command is required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if expiry, exists := fields["expires_at"]; exists && bytes.Equal(bytes.TrimSpace(expiry), []byte("null")) {
		return Fail(Invalid, "message expiry must be an explicit future Unix timestamp, not null")
	}
	responseProvided := false
	for name, response := range fields {
		if !strings.EqualFold(name, "response_requested") {
			continue
		}
		responseProvided = true
		if bytes.Equal(bytes.TrimSpace(response), []byte("null")) {
			return Fail(Invalid, "response_requested must be true or false, not null")
		}
	}
	if !responseProvided {
		value.ResponseRequest = value.Kind == "" || value.Kind == "direct" || value.Kind == "request"
	}
	*command = MessageCommand(value)
	return nil
}

func (command MessageCommand) ToRequest() MessageRequest {
	request := MessageRequest{
		Command: command.Command, RecipientID: command.RecipientID, Body: command.Body,
		Origin: command.Origin, Kind: command.Kind, Urgent: command.Urgent,
		ResponseRequest: command.ResponseRequest, ReplyToID: command.ReplyToID,
		Insistence: command.Insistence, Request: command.Request,
	}
	if command.ExpiresAt != nil {
		request.ExpiresAt = *command.ExpiresAt
	}
	return request
}

func (request *MessageRequest) UnmarshalJSON(data []byte) error {
	var command MessageCommand
	if err := json.Unmarshal(data, &command); err != nil {
		return err
	}
	*request = command.ToRequest()
	return nil
}

type RelayOpen struct {
	SessionID string `json:"session_id"`
	Side      string `json:"side"`
	Ticket    string `json:"ticket"`
}

type RelayChunk struct {
	Data []byte `json:"data"`
	EOF  bool   `json:"eof,omitempty"`
}

type RelayWritten struct {
	Written int `json:"written"`
}

type AccountState string

type ManagedAccount struct {
	AgentID              string       `json:"agent_id"`
	Username             string       `json:"username"`
	UID                  uint32       `json:"uid"`
	GID                  uint32       `json:"gid"`
	PublicKey            string       `json:"public_key"`
	Fingerprint          string       `json:"fingerprint"`
	PreviousFingerprints []string     `json:"previous_fingerprints,omitempty"`
	State                AccountState `json:"state"`
	Generation           int64        `json:"generation"`
}

type AccountProvisionRequest struct {
	AgentID    string `json:"agent_id"`
	Username   string `json:"username,omitempty"`
	PublicKey  string `json:"public_key"`
	Generation int64  `json:"generation"`
	Reactivate bool   `json:"reactivate,omitempty"`
}

type AccountDisableRequest struct {
	AgentID    string `json:"agent_id"`
	Username   string `json:"username,omitempty"`
	Generation int64  `json:"generation"`
}
