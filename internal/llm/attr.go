package llm

// Optional HTTP attribution labels (ignored by plain providers).
// Gateways that care can map these into their own observability slots.
const (
	HeaderActor     = "X-Mow-Actor"
	HeaderSession   = "X-Mow-Session"
	HeaderComponent = "X-Mow-Component"
)
