package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	ocpp16 "github.com/lorenzodonini/ocpp-go/ocpp1.6"
	"github.com/lorenzodonini/ocpp-go/ocpp1.6/core"
	"github.com/lorenzodonini/ocpp-go/ocpp1.6/types"
)

const defaultHeartbeatInterval = 600

type CSMSHandler struct {
	mu         sync.Mutex
	emspURL    string
	activeTxns map[int]*transactionInfo
}

type transactionInfo struct {
	ID           int
	ChargePoint  string
	IdTag        string
	ConnectorID  int
	MeterStart   int
	StartedAt    time.Time
	KwhDelivered float64
}

func newCSMSHandler(emspURL string) *CSMSHandler {
	return &CSMSHandler{
		emspURL:    emspURL,
		activeTxns: make(map[int]*transactionInfo),
	}
}

func (h *CSMSHandler) OnBootNotification(cpID string, req *core.BootNotificationRequest) (*core.BootNotificationConfirmation, error) {
	slog.Info("boot", "cp", cpID, "model", req.ChargePointModel, "vendor", req.ChargePointVendor)
	return core.NewBootNotificationConfirmation(types.NewDateTime(time.Now()), defaultHeartbeatInterval, core.RegistrationStatusAccepted), nil
}

func (h *CSMSHandler) OnAuthorize(cpID string, req *core.AuthorizeRequest) (*core.AuthorizeConfirmation, error) {
	slog.Info("authorize", "cp", cpID, "idTag", req.IdTag)

	allowed := h.verifyWithEMSP(req.IdTag)
	if allowed {
		slog.Info("authorize accepted", "cp", cpID, "idTag", req.IdTag)
		return core.NewAuthorizationConfirmation(types.NewIdTagInfo(types.AuthorizationStatusAccepted)), nil
	}
	slog.Warn("authorize rejected", "cp", cpID, "idTag", req.IdTag)
	return core.NewAuthorizationConfirmation(types.NewIdTagInfo(types.AuthorizationStatusInvalid)), nil
}

func (h *CSMSHandler) OnStartTransaction(cpID string, req *core.StartTransactionRequest) (*core.StartTransactionConfirmation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	txnID := len(h.activeTxns) + 1001
	h.activeTxns[txnID] = &transactionInfo{
		ID:          txnID,
		ChargePoint: cpID,
		IdTag:       req.IdTag,
		ConnectorID: req.ConnectorId,
		MeterStart:  req.MeterStart,
		StartedAt:   time.Now(),
	}

	slog.Info("transaction started", "cp", cpID, "txnID", txnID, "idTag", req.IdTag, "connector", req.ConnectorId)

	h.pushSessionToEMSP(txnID, "ACTIVE", 0)
	return core.NewStartTransactionConfirmation(types.NewIdTagInfo(types.AuthorizationStatusAccepted), txnID), nil
}

func (h *CSMSHandler) OnStopTransaction(cpID string, req *core.StopTransactionRequest) (*core.StopTransactionConfirmation, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	txn, ok := h.activeTxns[req.TransactionId]
	if !ok {
		slog.Warn("stop unknown transaction", "cp", cpID, "txnID", req.TransactionId)
		return core.NewStopTransactionConfirmation(), nil
	}

	kwh := float64(req.MeterStop-txn.MeterStart) / 1000.0
	if kwh < 0 {
		kwh = 0
	}
	costNOK := kwh * 2.50

	slog.Info("transaction stopped", "cp", cpID, "txnID", req.TransactionId, "kwh", kwh, "cost_nok", costNOK)

	h.pushSessionToEMSP(txn.ID, "COMPLETED", kwh)
	h.pushCDRToEMSP(txn, kwh, costNOK)

	delete(h.activeTxns, req.TransactionId)
	return core.NewStopTransactionConfirmation(), nil
}

func (h *CSMSHandler) OnMeterValues(cpID string, req *core.MeterValuesRequest) (*core.MeterValuesConfirmation, error) {
	for _, mv := range req.MeterValue {
		for _, sv := range mv.SampledValue {
			if sv.Measurand == types.MeasurandEnergyActiveExportRegister {
				slog.Debug("meter", "cp", cpID, "value", sv.Value, "unit", sv.Unit)
			}
		}
	}
	return core.NewMeterValuesConfirmation(), nil
}

func (h *CSMSHandler) OnStatusNotification(cpID string, req *core.StatusNotificationRequest) (*core.StatusNotificationConfirmation, error) {
	slog.Info("status", "cp", cpID, "connector", req.ConnectorId, "status", req.Status)
	return core.NewStatusNotificationConfirmation(), nil
}

func (h *CSMSHandler) OnHeartbeat(cpID string, req *core.HeartbeatRequest) (*core.HeartbeatConfirmation, error) {
	return core.NewHeartbeatConfirmation(types.NewDateTime(time.Now())), nil
}

func (h *CSMSHandler) OnDataTransfer(cpID string, req *core.DataTransferRequest) (*core.DataTransferConfirmation, error) {
	return core.NewDataTransferConfirmation(core.DataTransferStatusAccepted), nil
}

func (h *CSMSHandler) verifyWithEMSP(idTag string) bool {
	url := fmt.Sprintf("%s/ocpi/emsp/2.2.1/tokens/%s/authorize", h.emspURL, idTag)
	resp, err := http.Post(url, "application/json", nil)
	if err != nil {
		slog.Error("emsp authorize request failed", "url", url, "error", err)
		return true // fail open for demo (charger still works if eMSP is down)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result struct {
		StatusCode int `json:"status_code"`
		Data       struct {
			Allowed string `json:"allowed"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		slog.Error("emsp authorize parse failed", "error", err, "body", string(body)[:200])
		return true
	}

	slog.Info("emsp authorize result", "allowed", result.Data.Allowed, "status", result.StatusCode)
	return result.Data.Allowed == "ALLOWED"
}

func (h *CSMSHandler) pushSessionToEMSP(txnID int, status string, kwh float64) {
	txn, ok := h.activeTxns[txnID]
	if !ok {
		return
	}
	session := map[string]interface{}{
		"id":          fmt.Sprintf("sess-csms-%d", txnID),
		"auth_id":     txn.IdTag,
		"status":      status,
		"kwh":         kwh,
		"location_id": "csms-virtual",
	}
	h.postJSON(fmt.Sprintf("%s/ocpi/emsp/2.2.1/sessions/sess-csms-%d", h.emspURL, txnID), session)
}

func (h *CSMSHandler) pushCDRToEMSP(txn *transactionInfo, kwh, cost float64) {
	cdr := map[string]interface{}{
		"id":          fmt.Sprintf("cdr-csms-%d", txn.ID),
		"auth_id":     txn.IdTag,
		"kwh":         kwh,
		"total_cost":  cost,
		"currency":    "NOK",
		"location_id": "csms-virtual",
		"start_date":  txn.StartedAt.UTC().Format(time.RFC3339),
		"stop_date":   time.Now().UTC().Format(time.RFC3339),
	}
	h.postJSON(fmt.Sprintf("%s/ocpi/emsp/2.2.1/cdrs", h.emspURL), cdr)
}

func (h *CSMSHandler) postJSON(url string, body map[string]interface{}) {
	data, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytesReader(data))
	if err != nil {
		slog.Error("emsp push failed", "url", url, "error", err)
		return
	}
	resp.Body.Close()
	slog.Info("emsp push ok", "url", url, "status", resp.StatusCode)
}

func bytesReader(data []byte) io.Reader {
	return &bytesReaderImpl{data: data}
}

type bytesReaderImpl struct {
	data []byte
	pos  int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	port := flag.Int("port", 8887, "WebSocket listen port")
	emspURL := flag.String("emsp", os.Getenv("EMSP_URL"), "eMSP base URL (e.g., https://ocpi.nodns.shop)")
	flag.Parse()

	if *emspURL == "" {
		*emspURL = "https://ocpi.nodns.shop"
	}

	handler := newCSMSHandler(*emspURL)

	cs := ocpp16.NewCentralSystem(nil, nil)
	cs.SetCoreHandler(handler)

	cs.SetNewChargePointHandler(func(cp ocpp16.ChargePointConnection) {
		slog.Info("charge point connected", "id", cp.ID())
	})
	cs.SetChargePointDisconnectedHandler(func(cp ocpp16.ChargePointConnection) {
		slog.Info("charge point disconnected", "id", cp.ID())
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("CSMS starting", "port", *port, "emsp", *emspURL)
		cs.Start(*port, "/{ws}")
	}()

	<-ctx.Done()
	slog.Info("shutting down")
}
