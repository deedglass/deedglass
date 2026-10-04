package stripe

import (
	"encoding/json"

	"github.com/deedglass/deedglass/internal/connector"
)

// Tool names (unqualified).
const (
	ToolGetCustomer            = "get_customer"
	ToolListCharges            = "list_charges"
	ToolGetCharge              = "get_charge"
	ToolGetPaymentIntent       = "get_payment_intent"
	ToolListRefunds            = "list_refunds"
	ToolGetSubscription        = "get_subscription"
	ToolCreateRefund           = "create_refund"
	ToolCreateInvoiceItem      = "create_invoice_item"
	ToolUpdateCustomerMetadata = "update_customer_metadata"
	ToolCancelSubscription     = "cancel_subscription"
)

// L1 sentences (S30 §3.1): set on the writes, so the Witness runs NativeReadBack.
const (
	L1CreateRefund      = "Stripe shows the refund for the same amount and currency on the same charge, status succeeded or pending."
	L1CreateInvoiceItem = "Stripe shows the invoice item with the same amount, currency and customer."
	L1UpdateMetadata    = "Every key has the requested value, and deleted keys are gone."
	L1Cancel            = "Stripe shows the subscription canceled, or set to cancel at the period end."
)

// Id patterns.
const (
	patCustomer     = `^cus_[A-Za-z0-9]{8,64}$`
	patCharge       = `^ch_[A-Za-z0-9]{8,64}$`
	patIntent       = `^pi_[A-Za-z0-9]{8,64}$`
	patSubscription = `^sub_[A-Za-z0-9]{8,64}$`
)

func idArg(pat, desc string) string {
	return `{"type":"string","pattern":` + js(pat) + `,"description":` + js(desc) + `}`
}

const amountArg = `{"type":"integer","minimum":1,"maximum":99999999,"description":"Minor units of the charge's currency: 2000 is $20.00, 5000 is ¥5,000."}`
const metadataArg = `{"type":"object","description":"Up to 20 keys (letters, digits, _ and -, at most 40 characters) with string values of at most 500 characters. Keys starting with deedglass_ are reserved."}`

func toolSpecs() []connector.ToolSpec {
	obj := func(req []string, props string) json.RawMessage {
		r, _ := json.Marshal(req)
		return json.RawMessage(`{"type":"object","additionalProperties":false,"required":` + string(r) + `,"properties":{` + props + `}}`)
	}
	limit := `"limit":{"type":"integer","minimum":1,"maximum":25,"description":"At most 25."}`
	return []connector.ToolSpec{
		{Name: ToolGetCustomer, Description: "Read a customer: id, name, email and metadata.",
			InputSchema: obj([]string{"customer"}, `"customer":`+idArg(patCustomer, "The customer id, cus_…")),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolListCharges, Description: "List recent charges, optionally for one customer (at most 25).",
			InputSchema: obj([]string{}, `"customer":`+idArg(patCustomer, "Only this customer's charges.")+`,`+limit+`,"starting_after":`+idArg(patCharge, "The last charge id of the previous page.")),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolGetCharge, Description: "Read a charge: amount, currency, what was refunded and whether it is disputed.",
			InputSchema: obj([]string{"charge"}, `"charge":`+idArg(patCharge, "The charge id, ch_…")),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolGetPaymentIntent, Description: "Read a PaymentIntent: amount, currency, status and its latest charge.",
			InputSchema: obj([]string{"payment_intent"}, `"payment_intent":`+idArg(patIntent, "The PaymentIntent id, pi_…")),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolListRefunds, Description: "List the refunds of one charge or PaymentIntent (at most 25).",
			InputSchema: obj([]string{}, `"charge":`+idArg(patCharge, "The charge id, ch_…")+`,"payment_intent":`+idArg(patIntent, "The PaymentIntent id, pi_…")+`,`+limit),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolGetSubscription, Description: "Read a subscription: status, price per period and when the period ends.",
			InputSchema: obj([]string{"subscription"}, `"subscription":`+idArg(patSubscription, "The subscription id, sub_…")),
			SideEffect:  connector.SideEffectRead, Idempotent: true},
		{Name: ToolCreateRefund, Description: "Refund part or all of a charge, in the charge's currency. Send charge or payment_intent, and amount in minor units.",
			InputSchema: obj([]string{"amount"}, `"charge":`+idArg(patCharge, "The charge to refund, ch_…")+`,"payment_intent":`+idArg(patIntent, "Or the PaymentIntent to refund, pi_…")+
				`,"amount":`+amountArg+`,"reason":{"type":"string","enum":["duplicate","fraudulent","requested_by_customer"]},"metadata":`+metadataArg),
			SideEffect: connector.SideEffectWrite, Idempotent: true, L1: L1CreateRefund},
		{Name: ToolCreateInvoiceItem, Description: "Add an item to a customer's next invoice.",
			InputSchema: obj([]string{"customer", "amount", "currency"}, `"customer":`+idArg(patCustomer, "The customer id, cus_…")+`,"amount":`+amountArg+
				`,"currency":{"type":"string","pattern":"^[a-z]{3}$","description":"Lowercase ISO 4217, like usd."},"description":{"type":"string","maxLength":500},"metadata":`+metadataArg),
			SideEffect: connector.SideEffectWrite, Idempotent: true, L1: L1CreateInvoiceItem},
		{Name: ToolUpdateCustomerMetadata, Description: "Set metadata keys on a customer. An empty string removes a key.",
			InputSchema: obj([]string{"customer", "metadata"}, `"customer":`+idArg(patCustomer, "The customer id, cus_…")+`,"metadata":`+metadataArg),
			SideEffect:  connector.SideEffectWrite, Idempotent: true, L1: L1UpdateMetadata},
		{Name: ToolCancelSubscription, Description: "Cancel a subscription at the end of its period (the default), or at once with at_period_end false.",
			InputSchema: obj([]string{"subscription"}, `"subscription":`+idArg(patSubscription, "The subscription id, sub_…")+`,"at_period_end":{"type":"boolean","description":"Default true."}`),
			SideEffect:  connector.SideEffectDestructive, Idempotent: true, L1: L1Cancel},
	}
}

func js(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
