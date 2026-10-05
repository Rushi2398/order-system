package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/Rushi2398/order-system/services/order-service/internal/models"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrderHandler struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

func NewOrderHandler(pool *pgxpool.Pool, logger *slog.Logger) *OrderHandler {
	return &OrderHandler{pool: pool, logger: logger}
}

// apiError is the shape every error response takes. Consistency here is
// what lets a frontend (or a future API consumer) handle failures
// generically instead of special-casing each endpoint's error format.
type apiError struct {
	Error string `json:"error"`
}

func respondError(c *gin.Context, status int, message string) {
	c.JSON(status, apiError{Error: message})
}

// CreateOrder inserts an order and its items in a single transaction.
// The transaction boundary matters here specifically: if the order insert
// succeeds but an item insert fails partway through, a customer would
// otherwise end up with an order that silently has fewer items than they
// asked for -- rolling back on any failure is what prevents that.
func (h *OrderHandler) CreateOrder(c *gin.Context) {
	var req models.CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	ctx := c.Request.Context()
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		h.logger.Error("begin transaction failed", "error", err)
		respondError(c, http.StatusInternalServerError, "could not create order")
		return
	}
	// Defer a rollback that's a no-op once we commit successfully below.
	// This is the standard pgx pattern for "roll back on any early return".
	defer tx.Rollback(ctx)

	var totalAmount float64
	for _, item := range req.Items {
		totalAmount += float64(item.Quantity) * item.UnitPrice
	}

	var order models.Order
	err = tx.QueryRow(ctx,
		`INSERT INTO orders (customer_name, status, total_amount)
		 VALUES ($1, 'pending', $2)
		 RETURNING id, customer_name, status, total_amount, created_at, updated_at`,
		req.CustomerName, totalAmount,
	).Scan(&order.ID, &order.CustomerName, &order.Status, &order.TotalAmount, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		h.logger.Error("insert order failed", "error", err)
		respondError(c, http.StatusInternalServerError, "could not create order")
		return
	}

	for _, item := range req.Items {
		var inserted models.OrderItem
		err = tx.QueryRow(ctx,
			`INSERT INTO order_items (order_id, product_name, quantity, unit_price)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id, product_name, quantity, unit_price`,
			order.ID, item.ProductName, item.Quantity, item.UnitPrice,
		).Scan(&inserted.ID, &inserted.ProductName, &inserted.Quantity, &inserted.UnitPrice)
		if err != nil {
			h.logger.Error("insert order item failed", "product", item.ProductName, "error", err)
			respondError(c, http.StatusInternalServerError, "could not create order")
			return
		}
		order.Items = append(order.Items, inserted)
	}

	if err := tx.Commit(ctx); err != nil {
		h.logger.Error("commit transaction failed", "error", err)
		respondError(c, http.StatusInternalServerError, "could not create order")
		return
	}

	c.JSON(http.StatusCreated, order)
}

// GetOrder fetches a single order and its items. Two queries, not a join --
// deliberately, at this scale: a join here would return one row per item,
// which then needs de-duplicating in Go anyway. Worth revisiting if this
// endpoint ever shows up hot in a profiler.
func (h *OrderHandler) GetOrder(c *gin.Context) {
	idParam := c.Param("id")
	if _, err := uuid.Parse(idParam); err != nil {
		respondError(c, http.StatusBadRequest, "invalid order id")
		return
	}

	ctx := c.Request.Context()

	var order models.Order
	err := h.pool.QueryRow(ctx,
		`SELECT id, customer_name, status, total_amount, created_at, updated_at
		 FROM orders WHERE id = $1`,
		idParam,
	).Scan(&order.ID, &order.CustomerName, &order.Status, &order.TotalAmount, &order.CreatedAt, &order.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			respondError(c, http.StatusNotFound, "order not found")
			return
		}
		h.logger.Error("fetch order failed", "error", err)
		respondError(c, http.StatusInternalServerError, "could not fetch order")
		return
	}

	order.Items, err = h.fetchItems(ctx, order.ID)
	if err != nil {
		h.logger.Error("fetch order items failed", "error", err)
		respondError(c, http.StatusInternalServerError, "could not fetch order")
		return
	}

	c.JSON(http.StatusOK, order)
}

// ListOrders returns a page of orders, newest first. limit/offset are
// intentionally simple query params rather than cursor-based pagination --
// cursor pagination is the right call once this table has real volume
// needs to be refactored in later phases.
func (h *OrderHandler) ListOrders(c *gin.Context) {
	limit := 20
	if v := c.Query("limit"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 100 {
			limit = parsed
		}
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	ctx := c.Request.Context()

	rows, err := h.pool.Query(ctx,
		`SELECT id, customer_name, status, total_amount, created_at, updated_at
		 FROM orders ORDER BY created_at DESC LIMIT $1 OFFSET $2`,
		limit, offset,
	)
	if err != nil {
		h.logger.Error("list orders failed", "error", err)
		respondError(c, http.StatusInternalServerError, "could not list orders")
		return
	}
	defer rows.Close()

	orders := []models.Order{}
	for rows.Next() {
		var order models.Order
		if err := rows.Scan(&order.ID, &order.CustomerName, &order.Status, &order.TotalAmount, &order.CreatedAt, &order.UpdatedAt); err != nil {
			h.logger.Error("scan order failed", "error", err)
			respondError(c, http.StatusInternalServerError, "could not list orders")
			return
		}
		orders = append(orders, order)
	}

	// Items are fetched per-order here for the list endpoint. At real
	// traffic volume this N+1 pattern is exactly the kind of thing a load
	// test (Phase 7) surfaces -- worth deliberately noticing when it does.
	for i := range orders {
		items, err := h.fetchItems(ctx, orders[i].ID)
		if err != nil {
			h.logger.Error("fetch order items failed", "error", err)
			respondError(c, http.StatusInternalServerError, "could not list orders")
			return
		}
		orders[i].Items = items
	}

	c.JSON(http.StatusOK, gin.H{
		"orders": orders,
		"limit":  limit,
		"offset": offset,
	})
}

func (h *OrderHandler) fetchItems(ctx context.Context, orderID string) ([]models.OrderItem, error) {
	rows, err := h.pool.Query(ctx,
		`SELECT id, product_name, quantity, unit_price FROM order_items WHERE order_id = $1`,
		orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []models.OrderItem{}
	for rows.Next() {
		var item models.OrderItem
		if err := rows.Scan(&item.ID, &item.ProductName, &item.Quantity, &item.UnitPrice); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
