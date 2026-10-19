package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/KevVemn/junglegaming-backend/internal/config"
	"github.com/KevVemn/junglegaming-backend/internal/domain"
	"github.com/KevVemn/junglegaming-backend/internal/observability"
	"github.com/KevVemn/junglegaming-backend/internal/postgres"
	"github.com/KevVemn/junglegaming-backend/internal/service"
)

type Handler struct {
	wallets     *service.WalletService
	wagering    *service.WageringService
	health      *HealthChecker
	metrics     *observability.Metrics
	authEnabled bool
}

func NewHandler(
	wallets *service.WalletService,
	wagering *service.WageringService,
	health *HealthChecker,
	metrics *observability.Metrics,
	authCfg config.AuthConfig,
) *Handler {
	return &Handler{
		wallets:     wallets,
		wagering:    wagering,
		health:      health,
		metrics:     metrics,
		authEnabled: authCfg.Enabled,
	}
}

func (h *Handler) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	mux.Handle("GET /metrics", promhttp.HandlerFor(
		h.metrics.Registry(), promhttp.HandlerOpts{EnableOpenMetrics: true}))

	mux.HandleFunc("GET /health/live", h.live)
	mux.HandleFunc("GET /health/ready", h.ready)

	mux.HandleFunc("POST /wallets", h.openWallet)
	mux.HandleFunc("GET /wallets/{walletId}", h.getWallet)
	mux.HandleFunc("GET /wallets/{walletId}/ledger", h.getLedger)
	mux.HandleFunc("POST /wallets/{walletId}/reconciliation", h.reconcile)

	mux.HandleFunc("POST /wagering/transactions", h.submitTransaction)
	mux.HandleFunc("GET /wagering/transactions/{transactionId}", h.getTransaction)
	mux.HandleFunc("GET /providers/{providerId}/wagering/transactions/{externalTransactionId}", h.getProviderTransaction)

	return mux
}

func (h *Handler) live(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, HealthResponse{Status: "ok"})
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	checks, healthy := h.health.Check(r.Context())
	status := http.StatusOK
	state := "ok"
	if !healthy {
		status = http.StatusServiceUnavailable
		state = "degraded"
	}
	writeJSON(w, status, HealthResponse{Status: state, Checks: checks})
}

func (h *Handler) openWallet(w http.ResponseWriter, r *http.Request) {
	if !h.isInternalCaller(r) {
		writeError(w, r, http.StatusForbidden, "INTERNAL_ONLY",
			"wallet operations are restricted to internal service identities")
		return
	}

	var req OpenWalletRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PLAYER_ID", "playerId must be a valid uuid")
		return
	}
	if req.InitialBalance == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "initialBalance is required")
		return
	}

	wallet, err := h.wallets.Open(r.Context(), service.OpenWalletCommand{
		PlayerID:       playerID,
		InitialBalance: *req.InitialBalance,
		CorrelationID:  CorrelationIDFrom(r.Context()),
	})
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	writeJSON(w, http.StatusCreated, walletResponse(wallet))
}

func (h *Handler) getWallet(w http.ResponseWriter, r *http.Request) {
	walletID, ok := pathUUID(w, r, "walletId")
	if !ok {
		return
	}

	wallet, err := h.wallets.GetByID(r.Context(), walletID)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, walletResponse(wallet))
}

func (h *Handler) getLedger(w http.ResponseWriter, r *http.Request) {
	walletID, ok := pathUUID(w, r, "walletId")
	if !ok {
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	page, err := h.wallets.Ledger(r.Context(), walletID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, ledgerPageResponse(page))
}

func (h *Handler) reconcile(w http.ResponseWriter, r *http.Request) {
	if !h.isInternalCaller(r) {
		writeError(w, r, http.StatusForbidden, "INTERNAL_ONLY",
			"reconciliation is restricted to internal service identities")
		return
	}

	walletID, ok := pathUUID(w, r, "walletId")
	if !ok {
		return
	}

	result, err := h.wallets.Reconcile(r.Context(), walletID)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, ReconciliationResponse{
		WalletID:          result.WalletID,
		StoredBalance:     result.StoredBalance,
		CalculatedBalance: result.CalculatedBalance,
		Difference:        result.Difference,
		Consistent:        result.Consistent,
		CheckedEntries:    result.CheckedEntries,
	})
}

func (h *Handler) submitTransaction(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get("Idempotency-Key")
	if idempotencyKey == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "Idempotency-Key header is required")
		return
	}

	var req SubmitTransactionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	playerID, err := uuid.Parse(req.PlayerID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PLAYER_ID", "playerId must be a valid uuid")
		return
	}
	walletID, err := uuid.Parse(req.WalletID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_WALLET_ID", "walletId must be a valid uuid")
		return
	}
	if req.Money == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "money is required")
		return
	}

	authenticated := ProviderIDFrom(r.Context())
	if authenticated != "" && req.ProviderID != authenticated {
		writeError(w, r, http.StatusForbidden, "PROVIDER_FORBIDDEN",
			"authenticated identity does not match providerId")
		return
	}

	result, err := h.wagering.Submit(r.Context(), service.SubmitCommand{
		ProviderID:            req.ProviderID,
		ExternalTransactionID: req.ExternalTransactionID,
		IdempotencyKey:        idempotencyKey,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               req.RoundID,
		GameID:                req.GameID,
		Kind:                  domain.TransactionKind(req.Kind),
		Money:                 *req.Money,
		ReferenceExternalID:   req.ReferenceExternalID,
		CorrelationID:         CorrelationIDFrom(r.Context()),
	})
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	status := http.StatusOK
	if !result.IdempotentReplay {
		switch result.State {
		case domain.StateProcessed:
			status = http.StatusCreated
		case domain.StatePendingReference, domain.StatePending:
			status = http.StatusAccepted
		case domain.StateRejected:
			status = http.StatusUnprocessableEntity
		}
	}

	writeJSON(w, status, submitResponse(result))
}

func (h *Handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	transactionID, ok := pathUUID(w, r, "transactionId")
	if !ok {
		return
	}

	transaction, err := h.wagering.GetByID(r.Context(), transactionID, ProviderIDFrom(r.Context()))
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, transactionResponse(transaction))
}

func (h *Handler) getProviderTransaction(w http.ResponseWriter, r *http.Request) {
	providerID := r.PathValue("providerId")
	externalID := r.PathValue("externalTransactionId")

	authenticated := ProviderIDFrom(r.Context())
	if authenticated != "" && providerID != authenticated {
		writeError(w, r, http.StatusForbidden, "PROVIDER_FORBIDDEN",
			"authenticated identity cannot access another provider")
		return
	}

	transaction, err := h.wagering.GetByExternalID(r.Context(), providerID, externalID)
	if err != nil {
		writeDomainError(w, r, err)
		return
	}

	writeJSON(w, http.StatusOK, transactionResponse(transaction))
}

func (h *Handler) isInternalCaller(r *http.Request) bool {
	if !h.authEnabled {
		return true
	}
	return IsInternalCaller(r.Context())
}

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (uuid.UUID, bool) {
	parsed, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_UUID", name+" must be a valid uuid")
		return uuid.Nil, false
	}
	return parsed, true
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		code, message := classifyDecodeError(err)
		writeError(w, r, http.StatusBadRequest, code, message)
		return false
	}
	return true
}

func classifyDecodeError(err error) (string, string) {
	switch {
	case errors.Is(err, domain.ErrInvalidMoneyFormat):
		return "INVALID_MONEY_FORMAT", "money amount must be a decimal string with up to two places"
	case errors.Is(err, domain.ErrNegativeAmount):
		return "NEGATIVE_AMOUNT", "money amount must not be negative"
	case errors.Is(err, domain.ErrInvalidCurrency):
		return "INVALID_CURRENCY", "currency must be a three letter ISO 4217 code"
	default:
		return "INVALID_BODY", "request body is not valid json for this endpoint"
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, ErrorResponse{
		Code:          code,
		Message:       message,
		CorrelationID: CorrelationIDFrom(r.Context()),
	})
}

func writeDomainError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, postgres.ErrWalletNotFound):
		writeError(w, r, http.StatusNotFound, "WALLET_NOT_FOUND", "wallet does not exist")

	case errors.Is(err, postgres.ErrTransactionNotFound):
		writeError(w, r, http.StatusNotFound, "TRANSACTION_NOT_FOUND", "transaction does not exist")

	case errors.Is(err, postgres.ErrWalletAlreadyExists):
		writeError(w, r, http.StatusConflict, "WALLET_ALREADY_EXISTS",
			"a wallet already exists for this player and currency")

	case errors.Is(err, service.ErrIdempotencyConflict):
		writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT",
			"idempotency key was reused with a different payload")

	case errors.Is(err, service.ErrProviderForbidden):
		writeError(w, r, http.StatusForbidden, "PROVIDER_FORBIDDEN",
			"provider cannot access this transaction")

	case errors.Is(err, service.ErrWalletMismatch):
		writeError(w, r, http.StatusUnprocessableEntity, "WALLET_PLAYER_MISMATCH",
			"wallet does not belong to the informed player")

	case errors.Is(err, postgres.ErrConcurrencyConflict):
		w.Header().Set("Retry-After", "1")
		writeError(w, r, http.StatusConflict, "CONCURRENCY_CONFLICT",
			"wallet was modified concurrently, retry the request")

	case errors.Is(err, postgres.ErrInvalidCursor):
		writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "pagination cursor is not valid")

	case errors.Is(err, domain.ErrInvalidMoneyFormat),
		errors.Is(err, domain.ErrNegativeAmount),
		errors.Is(err, domain.ErrInvalidCurrency),
		errors.Is(err, domain.ErrNonPositiveAmount),
		errors.Is(err, domain.ErrCurrencyMismatch),
		errors.Is(err, domain.ErrInvalidTransactionKind),
		errors.Is(err, domain.ErrReferenceRequired),
		errors.Is(err, domain.ErrReferenceNotAllowed),
		errors.Is(err, domain.ErrInternalKindNotAllowed):
		writeError(w, r, http.StatusUnprocessableEntity, "BUSINESS_RULE_VIOLATION", err.Error())

	default:
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR",
			"unexpected error while processing the request")
	}
}
