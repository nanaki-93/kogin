package app

type AgentCostEstimate struct {
	Status        string   `json:"status"`
	Currency      string   `json:"currency,omitempty"`
	PriceVersion  string   `json:"price_version,omitempty"`
	KnownSubtotal float64  `json:"known_subtotal"`
	Total         *float64 `json:"total,omitempty"`
	Reasons       []string `json:"reasons"`
}
