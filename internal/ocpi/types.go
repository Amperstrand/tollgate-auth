// Package ocpi implements an OCPI 2.2.1 eMSP Receiver for tollgate-auth.
//
// It exposes the receiver endpoints a CPO (or OCPI test platform like OCPPLab)
// calls during roaming: versions, credentials handshake, locations/sessions/CDRs
// push, and real-time token authorize. The authorize endpoint delegates to the
// existing internal/auth.ProcessAuth pipeline so the same Cashu payment gate
// that powers SSH and RADIUS now gates EV charging.
package ocpi

import "time"

// OCPI 2.2.1 protocol version this package implements.
const VersionNumber = "2.2.1"

// InterfaceRole identifies which role advertises a module endpoint.
type InterfaceRole string

const (
	RoleSender   InterfaceRole = "SENDER"
	RoleReceiver InterfaceRole = "RECEIVER"
)

// StatusCode is the OCPI response status code (see OCPI 2.2.1 §8).
type StatusCode int

const (
	StatusSuccess              StatusCode = 1000
	StatusClientError          StatusCode = 2000
	StatusInvalidOrMissingRW   StatusCode = 2001
	StatusUnknownMethod        StatusCode = 2003
	StatusInvalidVersion       StatusCode = 2004
	StatusNoMatchingElement    StatusCode = 2005
	StatusServerBusy           StatusCode = 3000
	StatusInternalError        StatusCode = 3001
	StatusNotImplemented       StatusCode = 3002
	StatusHubGenericError      StatusCode = 4000
	StatusHubUnknownReceiver   StatusCode = 4001
	StatusHubTimeout           StatusCode = 4002
	StatusHubConnectionProblem StatusCode = 4003
)

// Response is the standard OCPI response envelope.
type Response struct {
	Status     StatusCode `json:"status"`
	StatusCode StatusCode `json:"status_code"`
	StatusMsg  string     `json:"status_message,omitempty"`
	Timestamp  time.Time  `json:"timestamp"`
	Data       any        `json:"data,omitempty"`
}

func newResponse(code StatusCode, msg string, data any) Response {
	return Response{Status: code, StatusCode: code, StatusMsg: msg, Timestamp: time.Now().UTC(), Data: data}
}

// OK wraps data in a 1000 success response.
func OK(data any) Response { return newResponse(StatusSuccess, "", data) }

// Err wraps an error in a 2000 client error response.
func Err(code StatusCode, msg string) Response { return newResponse(code, msg, nil) }

// VersionDetail is the response from GET /ocpi/{version}/version_details.
type VersionDetail struct {
	Version   string           `json:"version"`
	Endpoints []ModuleEndpoint `json:"endpoints"`
}

// ModuleEndpoint is one entry in the version detail endpoint list.
type ModuleEndpoint struct {
	Identifier string        `json:"identifier"`
	URL        string        `json:"url"`
	Role       InterfaceRole `json:"role"`
}

// Version represents one supported version advertised at /ocpi/versions.
type Version struct {
	Version string `json:"version"`
	URL     string `json:"url"`
}

// VersionsResponse is the response from GET /ocpi/versions.
type VersionsResponse struct {
	Versions []Version `json:"versions"`
}

// Credentials is the OCPI Credentials DTO used during the handshake.
type Credentials struct {
	Token    string `json:"token"`
	URL      string `json:"url"`
	PartyID  string `json:"party_id"`
	Country  string `json:"country_code"`
	Business string `json:"business_role,omitempty"`
}

// TokenType enumerates OCPI token types.
type TokenType string

const (
	TokenTypeRFID  TokenType = "RFID"
	TokenTypeApp   TokenType = "APP_USER"
	TokenTypeOther TokenType = "OTHER"
)

// WhitelistType controls how CPOs handle a token.
type WhitelistType string

const (
	WhitelistAlways  WhitelistType = "ALWAYS"
	WhitelistNever   WhitelistType = "NEVER"
	WhitelistAllowed WhitelistType = "ALLOWED"
)

// Token is the OCPI Token DTO (eMSP issuer side).
type Token struct {
	UID             string        `json:"uid"`
	Type            TokenType     `json:"type"`
	ContractID      string        `json:"contract_id"`
	VisualNumber    string        `json:"visual_number,omitempty"`
	Issuer          string        `json:"issuer"`
	Valid           bool          `json:"valid"`
	Whitelist       WhitelistType `json:"whitelist"`
	LastUsed        *time.Time    `json:"last_used,omitempty"`
	Language        string        `json:"language,omitempty"`
	ProfilePriority string        `json:"profile_priority,omitempty"`
}

// AuthorizationStatus is the result of a real-time authorize call.
type AuthorizationStatus string

const (
	AuthzAllowed    AuthorizationStatus = "ALLOWED"
	AuthzDisallowed AuthorizationStatus = "DISALLOWED"
	AuthzUnknown    AuthorizationStatus = "UNKNOWN"
)

// AuthorizeResponse is returned from POST /tokens/{uid}/authorize.
type AuthorizeResponse struct {
	Allowed AuthorizationStatus `json:"allowed"`
	// AuthorizationReference ties this authorize back to our internal session.
	AuthorizationReference string `json:"authorization_reference,omitempty"`
	// InfoURL is shown to the driver when payment is required.
	InfoURL string `json:"info_url,omitempty"`
}

// Session is a minimal OCPI Session DTO (subset we receive).
type Session struct {
	ID           string     `json:"id"`
	Started      time.Time  `json:"start_datetime"`
	Stopped      *time.Time `json:"stop_datetime,omitempty"`
	Kwh          float64    `json:"kwh"`
	AuthID       string     `json:"auth_id"`
	AuthMethod   string     `json:"auth_method,omitempty"`
	LocationID   string     `json:"location_id"`
	EvseUID      string     `json:"evse_uid,omitempty"`
	ConnectorID  string     `json:"connector_id,omitempty"`
	Currency     string     `json:"currency,omitempty"`
	TotalCost    float64    `json:"total_cost,omitempty"`
	CreditAmount int        `json:"credit_amount,omitempty"`
	Unit         string     `json:"unit,omitempty"`
	Status       string     `json:"status"`
	LastUpdated  time.Time  `json:"last_updated"`
}

// CDR is a minimal OCPI Charge Detail Record (subset we receive).
type CDR struct {
	ID           string    `json:"id"`
	Started      time.Time `json:"start_date"`
	Stopped      time.Time `json:"stop_date"`
	AuthID       string    `json:"auth_id"`
	LocationID   string    `json:"location_id"`
	EvseUID      string    `json:"evse_uid,omitempty"`
	ConnectorID  string    `json:"connector_id,omitempty"`
	Kwh          float64   `json:"total_kwh"`
	TotalCost    float64   `json:"total_cost"`
	Currency     string    `json:"currency,omitempty"`
	CreditAmount int       `json:"credit_amount,omitempty"`
	CreditUsed   float64   `json:"credit_used,omitempty"`
	Unit         string    `json:"unit,omitempty"`
	LastUpdated  time.Time `json:"last_updated"`
}
