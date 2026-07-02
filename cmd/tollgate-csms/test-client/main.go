package main

import (
	"log"
	"time"

	ocpp16 "github.com/lorenzodonini/ocpp-go/ocpp1.6"
	"github.com/lorenzodonini/ocpp-go/ocpp1.6/core"
	"github.com/lorenzodonini/ocpp-go/ocpp1.6/types"
)

type TestChargePointHandler struct{}

func (h *TestChargePointHandler) OnChangeAvailability(req *core.ChangeAvailabilityRequest) (*core.ChangeAvailabilityConfirmation, error) {
	return core.NewChangeAvailabilityConfirmation(core.AvailabilityStatusAccepted), nil
}

func (h *TestChargePointHandler) OnChangeConfiguration(req *core.ChangeConfigurationRequest) (*core.ChangeConfigurationConfirmation, error) {
	return core.NewChangeConfigurationConfirmation(core.ConfigurationStatusAccepted), nil
}

func (h *TestChargePointHandler) OnClearCache(req *core.ClearCacheRequest) (*core.ClearCacheConfirmation, error) {
	return core.NewClearCacheConfirmation(core.ClearCacheStatusAccepted), nil
}

func (h *TestChargePointHandler) OnDataTransfer(req *core.DataTransferRequest) (*core.DataTransferConfirmation, error) {
	return core.NewDataTransferConfirmation(core.DataTransferStatusAccepted), nil
}

func (h *TestChargePointHandler) OnGetConfiguration(req *core.GetConfigurationRequest) (*core.GetConfigurationConfirmation, error) {
	return core.NewGetConfigurationConfirmation(nil), nil
}

func (h *TestChargePointHandler) OnRemoteStartTransaction(req *core.RemoteStartTransactionRequest) (*core.RemoteStartTransactionConfirmation, error) {
	return core.NewRemoteStartTransactionConfirmation(types.RemoteStartStopStatusAccepted), nil
}

func (h *TestChargePointHandler) OnRemoteStopTransaction(req *core.RemoteStopTransactionRequest) (*core.RemoteStopTransactionConfirmation, error) {
	return core.NewRemoteStopTransactionConfirmation(types.RemoteStartStopStatusAccepted), nil
}

func (h *TestChargePointHandler) OnReset(req *core.ResetRequest) (*core.ResetConfirmation, error) {
	return core.NewResetConfirmation(core.ResetStatusAccepted), nil
}

func (h *TestChargePointHandler) OnUnlockConnector(req *core.UnlockConnectorRequest) (*core.UnlockConnectorConfirmation, error) {
	return core.NewUnlockConnectorConfirmation(core.UnlockStatusUnlocked), nil
}

func main() {
	csURL := "ws://localhost:8887"
	idTag := "OCPI-TEST-TOKEN"

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	cp := ocpp16.NewChargePoint("CP-TEST-001", nil, nil)
	cp.SetCoreHandler(&TestChargePointHandler{})

	log.Println("Connecting to CSMS at", csURL)
	err := cp.Start(csURL)
	if err != nil {
		log.Fatalf("Connection failed: %v", err)
	}
	log.Println("Connected to CSMS")

	bootConf, err := cp.BootNotification("TollgateTestModel", "TollgateVendor")
	if err != nil {
		log.Fatalf("BootNotification failed: %v", err)
	}
	log.Printf("BootNotification: status=%s interval=%d", bootConf.Status, bootConf.Interval)

	authConf, err := cp.Authorize(idTag)
	if err != nil {
		log.Fatalf("Authorize failed: %v", err)
	}
	log.Printf("Authorize: status=%s", authConf.IdTagInfo.Status)

	startConf, err := cp.StartTransaction(1, idTag, 0, types.NewDateTime(time.Now()))
	if err != nil {
		log.Fatalf("StartTransaction failed: %v", err)
	}
	log.Printf("StartTransaction: id=%d", startConf.TransactionId)

	meterVal := types.SampledValue{
		Value:     "5000",
		Unit:      types.UnitOfMeasureWh,
		Measurand: types.MeasurandEnergyActiveExportRegister,
		Context:   types.ReadingContextSamplePeriodic,
	}
	mv := types.MeterValue{Timestamp: types.NewDateTime(time.Now()), SampledValue: []types.SampledValue{meterVal}}
	_, err = cp.MeterValues(1, []types.MeterValue{mv})
	if err != nil {
		log.Printf("MeterValues error: %v", err)
	}
	log.Println("MeterValues sent: 5000 Wh")

	_, err = cp.StopTransaction(5000, types.NewDateTime(time.Now()), startConf.TransactionId)
	if err != nil {
		log.Fatalf("StopTransaction failed: %v", err)
	}
	log.Printf("StopTransaction complete. CDR should be at eMSP now.")

	cp.Stop()
	log.Println("Test cycle complete")
}
