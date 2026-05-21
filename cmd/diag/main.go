// Diagnostic tool: validates parser JSON manual against encoding/json.
//
// Compares vectorization results for example payloads and reports
// any discrepancies that could cause incorrect fraud detection.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"rinha-backend/internal/model"
	"rinha-backend/internal/parser"
	"rinha-backend/internal/vector"
)

// normalizeFromStdlib parses with encoding/json and returns the vector.
func normalizeFromStdlib(body []byte, norm *vector.NormalizationConfig) vector.Vector14 {
	var tx model.TransactionPayload
	if err := json.Unmarshal(body, &tx); err != nil {
		panic(fmt.Sprintf("stdlib parse failed: %v", err))
	}

	// Convert to parser.Payload
	p := parser.Payload{
		Amount:             tx.Transaction.Amount,
		Installments:       tx.Transaction.Installments,
		RequestedAt:        tx.Transaction.RequestedAt,
		AvgAmount:          tx.Customer.AvgAmount,
		TxCount24h:         tx.Customer.TxCount24h,
		KnownMerchants:     tx.Customer.KnownMerchants,
		MerchantID:         tx.Merchant.ID,
		MCC:                tx.Merchant.MCC,
		MerchantAvgAmount:  tx.Merchant.AvgAmount,
		IsOnline:           tx.Terminal.IsOnline,
		CardPresent:        tx.Terminal.CardPresent,
		KmFromHome:         tx.Terminal.KmFromHome,
		HasLastTransaction: tx.LastTransaction != nil,
	}
	if tx.LastTransaction != nil {
		p.LastTimestamp = tx.LastTransaction.Timestamp
		p.LastKmFromCurrent = tx.LastTransaction.KmFromCurrent
	}

	return vector.Normalize(&p, norm)
}

// normalizeFromManual parses with the manual JSON parser and returns the vector.
func normalizeFromManual(body []byte, norm *vector.NormalizationConfig) vector.Vector14 {
	var p parser.Payload
	if err := parser.ParseJSON(body, &p); err != nil {
		panic(fmt.Sprintf("manual parse failed: %v", err))
	}
	return vector.Normalize(&p, norm)
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: diag <payloads.json>\n")
		os.Exit(1)
	}

	payloadsPath := os.Args[1]
	data, err := os.ReadFile(payloadsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read %s: %v\n", payloadsPath, err)
		os.Exit(1)
	}

	var payloads []json.RawMessage
	if err := json.Unmarshal(data, &payloads); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse payloads: %v\n", err)
		os.Exit(1)
	}

	// Load normalization config
	resourcesDir := "resources"
	if dir := os.Getenv("RESOURCES_DIR"); dir != "" {
		resourcesDir = dir
	}

	normData, err := os.ReadFile(resourcesDir + "/normalization.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read normalization.json: %v\n", err)
		os.Exit(1)
	}
	var normModel model.Normalization
	if err := json.Unmarshal(normData, &normModel); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse normalization.json: %v\n", err)
		os.Exit(1)
	}

	mccData, err := os.ReadFile(resourcesDir + "/mcc_risk.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read mcc_risk.json: %v\n", err)
		os.Exit(1)
	}
	var mccRisk map[string]float64
	if err := json.Unmarshal(mccData, &mccRisk); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse mcc_risk.json: %v\n", err)
		os.Exit(1)
	}

	norm := &vector.NormalizationConfig{
		MaxAmount:            normModel.MaxAmount,
		MaxInstallments:      normModel.MaxInstallments,
		AmountVsAvgRatio:     normModel.AmountVsAvgRatio,
		MaxMinutes:           normModel.MaxMinutes,
		MaxKm:                normModel.MaxKm,
		MaxTxCount24h:        normModel.MaxTxCount24h,
		MaxMerchantAvgAmount: normModel.MaxMerchantAvgAmount,
		MCCRisk:              mccRisk,
	}

	discrepancies := 0
	matches := 0

	for i, raw := range payloads {
		body := []byte(raw)

		// Parse with both methods
		vStd := normalizeFromStdlib(body, norm)
		vMan := normalizeFromManual(body, norm)

		// Compare
		if vStd != vMan {
			discrepancies++
			fmt.Printf("MISMATCH payload #%d:\n", i)
			fmt.Printf("  stdlib: %v\n", vStd)
			fmt.Printf("  manual: %v\n", vMan)

			// Show field-by-field differences
			for d := 0; d < 14; d++ {
				if vStd[d] != vMan[d] {
					fmt.Printf("  dim[%d]: stdlib=%d manual=%d\n", d, vStd[d], vMan[d])
				}
			}
			fmt.Println()
		} else {
			matches++
		}
	}

	fmt.Printf("Results: %d matches, %d mismatches out of %d payloads\n",
		matches, discrepancies, len(payloads))

	if discrepancies > 0 {
		os.Exit(1)
	}
}
