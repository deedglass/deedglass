package decoder

import "fmt"

// Console copy for the decoder (S26 renders it; S20 §5). One place, so the Console, the CLI's
// docs and this package say the same words.
const (
	// PanelNotWitnessed is the empty state of a Postgres connector's decoder panel.
	PanelNotWitnessed = "Checks aren't on for this connector yet. Turn them on to see what Postgres actually committed for every agent write. It needs wal_level=logical and a publication; the check below walks you through it."
	// toastForgedMarker is the alert toast for a forged marker; %s is the connector's name.
	toastForgedMarker = "Forged marker on %s: a transaction claimed to be a Deedglass action without a ledger row. Nothing was bound to it."
	// toastForgedNonTx is the toast for a forged non-transactional marker or heartbeat.
	toastForgedNonTx = "Forged message on %s: a message outside any transaction claimed to come from Deedglass. Nothing was bound to it."
	// toastForeignLedger is the toast for a ledger row of another org.
	toastForeignLedger = "Ledger row from another org on %s: a transaction named an action of a different org. Nothing was bound to it."
)

// RowLine is the decoder row on the Connectors screen: "Witness · healthy · 3.9 kB behind · last
// commit 4s ago". lag and ago are already formatted.
func RowLine(health, lag, ago string) string {
	s := "Witness · " + health
	if lag != "" {
		s += " · " + lag + " behind"
	}
	if ago != "" {
		s += " · last commit " + ago
	}
	return s
}

// AlertToast is the Console toast for a decoder.alert of kind on connector name.
func AlertToast(kind, name string) string {
	switch kind {
	case string(ClassForgedMarkerNonTx):
		return fmt.Sprintf(toastForgedNonTx, name)
	case string(ClassForeignLedger):
		return fmt.Sprintf(toastForeignLedger, name)
	}
	return fmt.Sprintf(toastForgedMarker, name)
}
