package models

import "time"

// Order is the domain model as read back from Postgres.
type Order struct {
	ID           string      `json:"id"`
	CustomerName string      `json:"customer_name"`
	Status       int64       `json:"status"`
	TotalAmount  float64     `json:"total_amount"`
	Items        []OrderItem `json:"items"`
	CreatedAt    time.Time   `json:"created_at"`
	UpdatedAt    time.Time   `json:"updated_at"`
}

type OrderItem struct {
	ID          string  `json:"id"`
	ProductName string  `json:"product_name"`
	Quantity    int     `json:"quantity"`
	UnitPrice   float64 `json:"unit_price"`
}

// CreateOrderRequest is the incoming payload for POST /orders. Validation
// tags are enforced by Gin's binding via go-playground/validator -- a
// malformed request never reaches the handler body, which is what keeps
// the handler itself focused on business logic instead of defensive checks.
type CreateOrderRequest struct {
	CustomerName string                   `json:"customer_name" binding:"required"`
	Items        []CreateOrderItemRequest `json:"items" binding:"required,min=1,dive"`
}

type CreateOrderItemRequest struct {
	ProductName string  `json:"product_name" binding:"required"`
	Quantity    int     `json:"quantity" binding:"required,gt=0"`
	UnitPrice   float64 `json:"unit_price" binding:"gte=0"`
}
