package model

import "time"

// TransactionPayload represents the incoming fraud detection request.
type TransactionPayload struct {
	ID          string              `json:"id"`
	Transaction TransactionData     `json:"transaction"`
	Customer    CustomerData        `json:"customer"`
	Merchant    MerchantData        `json:"merchant"`
	Terminal    TerminalData        `json:"terminal"`
	LastTransaction *LastTransactionData `json:"last_transaction"`
}

type TransactionData struct {
	Amount       float64   `json:"amount"`
	Installments int       `json:"installments"`
	RequestedAt  time.Time `json:"requested_at"`
}

type CustomerData struct {
	AvgAmount      float64  `json:"avg_amount"`
	TxCount24h     int      `json:"tx_count_24h"`
	KnownMerchants []string `json:"known_merchants"`
}

type MerchantData struct {
	ID        string  `json:"id"`
	MCC       string  `json:"mcc"`
	AvgAmount float64 `json:"avg_amount"`
}

type TerminalData struct {
	IsOnline    bool    `json:"is_online"`
	CardPresent bool    `json:"card_present"`
	KmFromHome  float64 `json:"km_from_home"`
}

type LastTransactionData struct {
	Timestamp     time.Time `json:"timestamp"`
	KmFromCurrent float64   `json:"km_from_current"`
}

// FraudScoreResponse is the response for the /fraud-score endpoint.
type FraudScoreResponse struct {
	Approved   bool    `json:"approved"`
	FraudScore float64 `json:"fraud_score"`
}

// Normalization constants loaded from normalization.json.
type Normalization struct {
	MaxAmount           float64 `json:"max_amount"`
	MaxInstallments     float64 `json:"max_installments"`
	AmountVsAvgRatio    float64 `json:"amount_vs_avg_ratio"`
	MaxMinutes          float64 `json:"max_minutes"`
	MaxKm               float64 `json:"max_km"`
	MaxTxCount24h       float64 `json:"max_tx_count_24h"`
	MaxMerchantAvgAmount float64 `json:"max_merchant_avg_amount"`
}