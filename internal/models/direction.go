package models

// Direction identifies the traffic direction an egress/ingress
// measurement, limit, or alert applies to.
type Direction string

// Traffic directions.
const (
	DirectionOut Direction = "out"
	DirectionIn  Direction = "in"
)
