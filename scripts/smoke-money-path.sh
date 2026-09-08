#!/usr/bin/env bash
#
# End-to-end smoke test for the Dupli1 money path:
#   catalog -> cart -> checkout session -> payment -> paid -> ship -> fulfilled
#
# Exercises the canonical /api/v1/{service}/... paths through the gateway and
# asserts the launch-critical invariants: prices come from the catalog (a bogus
# client unit_price_cents is ignored), stock is reserved at checkout and committed
# on ship, soldCount tracks the commit, payment bypass stays manager-only, and the
# removed `confirmed` status is rejected.
#
# Usage:
#   BASE=http://localhost:8080 scripts/smoke-money-path.sh          # creates a test product
#   BASE=https://dupli1.com SKU_ID=01J... scripts/smoke-money-path.sh  # uses an existing variant
#
# Environment:
#   BASE            gateway base URL (default http://localhost:8080)
#   OWNER_EMAIL     manager/owner login (default admin@dupli1.com)
#   OWNER_PASSWORD  manager/owner password (default password)
#   SKU_ID          existing variant to buy; when unset a throwaway product is created
#   QUANTITY        units to buy (default 1)
#
# The run creates a customer account and a real order. Without a card PG (NANO
# unconfigured, the default) it pays via manager Bypass.
set -uo pipefail

BASE=${BASE:-http://localhost:8080}
OWNER_EMAIL=${OWNER_EMAIL:-admin@dupli1.com}
OWNER_PASSWORD=${OWNER_PASSWORD:-password}
QUANTITY=${QUANTITY:-1}
SKU_ID=${SKU_ID:-}
JSON='Content-Type: application/json'

failures=0

step() { printf '\n\033[1m== %s\033[0m\n' "$1"; }
pass() { printf '  ok    %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

check() { # check <description> <actual> <expected>
  if [ "$2" = "$3" ]; then pass "$1 ($2)"; else fail "$1: got '$2', want '$3'"; fi
}

# read a field from JSON on stdin, e.g. field '["order"]["id"]'
field() { python3 -c "import json,sys;d=json.load(sys.stdin);print($1)" 2>/dev/null; }

api() { # api <method> <path> [token] [body]
  local method=$1 path=$2 token=${3:-} body=${4:-}
  local args=(-s -X "$method" "$BASE$path" -H "$JSON")
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}

status_of() { # status_of <method> <path> [token] [body]
  local method=$1 path=$2 token=${3:-} body=${4:-}
  local args=(-s -o /dev/null -w '%{http_code}' -X "$method" "$BASE$path" -H "$JSON")
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}"
}

access_token() { # access_token <email> <password>
  local refresh
  refresh=$(api POST /api/v1/auth/login "" "{\"email\":\"$1\",\"password\":\"$2\"}" | field 'd["refresh_token"]')
  [ -n "$refresh" ] || return 1
  api POST /api/v1/auth/refresh "" "{\"refresh_token\":\"$refresh\"}" | field 'd["token"]'
}

stock_of() { # stock_of <skuId> -> "quantity reserved"
  local json
  json=$(api GET "/api/v1/products/inventory/items/by-sku-id/$1")
  echo "$json" | field 'str(d["quantity"]) + " " + str(d["reserved"])'
}

step "Gateway and service health"
for svc in gateway/health api/v1/auth/health api/v1/products/health api/v1/orders/health \
           api/v1/cart/health api/v1/payments/health; do
  check "$svc" "$(status_of GET "/$svc")" 200
done

step "Manager sign-in"
OWNER=$(access_token "$OWNER_EMAIL" "$OWNER_PASSWORD")
if [ -z "$OWNER" ]; then
  fail "manager login as $OWNER_EMAIL"
  exit 1
fi
pass "manager access token acquired"

if [ -z "$SKU_ID" ]; then
  step "Seed a throwaway product (no SKU_ID given)"
  # brandCode + styleCode identify a parent product, so each run needs its own style.
  STYLE_CODE="SM$(date +%H%M%S)"
  # Master codes must exist before product/variant create; 409 means already seeded.
  for path_body in \
    "/api/v1/products/catalog/brands|{\"code\":\"SMK\",\"name\":\"Smoke Test\"}" \
    "/api/v1/products/catalog/brands/SMK/styles|{\"code\":\"$STYLE_CODE\",\"name\":\"Smoke Style\"}" \
    "/api/v1/products/catalog/colors|{\"code\":\"BLK\",\"name\":\"Black\"}" \
    "/api/v1/products/catalog/sizes|{\"code\":\"M\",\"name\":\"Medium\"}"
  do
    code=$(status_of POST "${path_body%%|*}" "$OWNER" "${path_body#*|}")
    case "$code" in
      201|409) pass "master data ${path_body%%|*} ($code)" ;;
      *) fail "master data ${path_body%%|*} returned $code" ;;
    esac
  done

  product=$(api POST /api/v1/products "$OWNER" "{
    \"name\":\"Smoke Test Bag\",\"description\":\"created by smoke-money-path.sh\",
    \"brand\":\"Smoke Test\",\"brandCode\":\"SMK\",\"styleCode\":\"$STYLE_CODE\",
    \"category\":\"bags\",\"subCategory\":\"cross\",\"style\":\"casual\",\"target\":\"women\",
    \"material\":\"leather\",\"price\":250000,\"officialPrice\":300000,\"status\":\"active\"
  }")
  PRODUCT_ID=$(echo "$product" | field 'd["id"]')
  [ -n "$PRODUCT_ID" ] && pass "product $PRODUCT_ID" || { fail "create product: $product"; exit 1; }

  variant=$(api POST "/api/v1/products/$PRODUCT_ID/variants" "$OWNER" \
    '{"colorCode":"BLK","sizeCode":"M","status":"active"}')
  SKU_ID=$(echo "$variant" | field 'd["skuId"]')
  [ -n "$SKU_ID" ] && pass "variant $(echo "$variant" | field 'd["sku"]') ($SKU_ID)" \
    || { fail "create variant: $variant"; exit 1; }

  check "set stock to 5" "$(api PUT "/api/v1/products/inventory/items/by-sku-id/$SKU_ID" "$OWNER" \
    '{"quantity":5}' | field 'd["quantity"]')" 5
else
  step "Using existing variant $SKU_ID"
  PRODUCT_ID=$(api GET "/api/v1/products/variants/by-sku-id/$SKU_ID" | field 'd["productId"]')
  pass "parent product $PRODUCT_ID"
fi

read -r stock_before reserved_before <<<"$(stock_of "$SKU_ID")"
pass "stock before: quantity=$stock_before reserved=$reserved_before"
sold_before=$(api GET "/api/v1/products/$PRODUCT_ID" | field 'd.get("soldCount",0)')

step "Customer sign-up and sign-in"
EMAIL="smoke-$(date +%s)-$RANDOM@example.com"
check "register" "$(status_of POST /api/v1/auth/register "" \
  "{\"email\":\"$EMAIL\",\"password\":\"smoke-test-password\"}")" 201
CUSTOMER=$(access_token "$EMAIL" "smoke-test-password")
CUSTOMER_ID=$(api GET /api/v1/auth/me "$CUSTOMER" | field 'd["user_id"]')
[ -n "$CUSTOMER_ID" ] && pass "customer $CUSTOMER_ID" || { fail "customer sign-in"; exit 1; }

step "Cart resolves price server-side"
# unit_price_cents below is deliberately wrong; the server must ignore it.
cart=$(api POST /api/v1/cart/items "$CUSTOMER" \
  "{\"sku_id\":\"$SKU_ID\",\"quantity\":$QUANTITY,\"unit_price_cents\":1}")
cart_price=$(echo "$cart" | field 'd["items"][0]["unit_price_cents"]')
catalog_price=$(api GET "/api/v1/products/$PRODUCT_ID" | field 'int(d["price"])')
check "cart unit price came from the catalog" "$cart_price" "$catalog_price"

step "Checkout session"
session=$(api POST /api/v1/orders/checkout/sessions "$CUSTOMER" "{\"customer_id\":\"$CUSTOMER_ID\"}")
SESSION_ID=$(echo "$session" | field 'd["id"]')
[ -n "$SESSION_ID" ] && pass "session $SESSION_ID" || { fail "create session: $session"; exit 1; }
session=$(api POST "/api/v1/orders/checkout/sessions/$SESSION_ID/items" "$CUSTOMER" \
  "{\"sku_id\":\"$SKU_ID\",\"quantity\":$QUANTITY,\"unit_price_cents\":1}")
check "session subtotal" "$(echo "$session" | field 'd["subtotal_cents"]')" "$((catalog_price * QUANTITY))"
session_fee=$(echo "$session" | field 'd.get("shipping_fee_krw", 0)')
check "session total includes shipping" \
  "$(echo "$session" | field 'd["total_cents"]')" \
  "$((catalog_price * QUANTITY + session_fee))"

step "Complete checkout"
completed=$(api POST "/api/v1/orders/checkout/sessions/$SESSION_ID/complete" "$CUSTOMER" \
  '{"recipient_name":"Smoke Test","recipient_phone":"01012345678","shipping_address":{"postal_code":"06236","address_line1":"123 Teheran-ro","city":"Gangnam-gu","province":"Seoul"}}')
ORDER_ID=$(echo "$completed" | field 'd["order"]["id"]')
[ -n "$ORDER_ID" ] && pass "order $ORDER_ID" || { fail "complete checkout: $completed"; exit 1; }
check "order status" "$(echo "$completed" | field 'd["order"]["status"]')" pending
# Read the delivery charge back rather than hard-coding it, so this passes at any
# configured DUPLI1_ORDER_SHIPPING_FEE_KRW (0 included).
shipping_fee=$(echo "$completed" | field 'd["order"].get("shipping_fee_krw", 0)')
check "order subtotal" "$(echo "$completed" | field 'd["order"]["subtotal_cents"]')" "$((catalog_price * QUANTITY))"
check "order total includes shipping" \
  "$(echo "$completed" | field 'd["order"]["total_cents"]')" \
  "$((catalog_price * QUANTITY + shipping_fee))"

read -r stock_reserved_q stock_reserved_r <<<"$(stock_of "$SKU_ID")"
check "stock reserved on checkout" "$stock_reserved_r" "$((reserved_before + QUANTITY))"

step "Payment bypass is manager-only"
check "customer bypass rejected" \
  "$(status_of POST /api/v1/payments "$CUSTOMER" \
    "{\"order_id\":\"$ORDER_ID\",\"method\":\"bypass\",\"note\":\"smoke test\"}")" 403

step "Pay"
# NANO card checkout needs a signed browser round-trip a curl script can't drive, so
# this always pays via manager Bypass (also the v1.0 launch default — no card PG yet).
pass "paying with manager Bypass"
payment=$(api POST /api/v1/payments "$OWNER" \
  "{\"order_id\":\"$ORDER_ID\",\"method\":\"bypass\",\"note\":\"smoke test\"}")
PAYMENT_ID=$(echo "$payment" | field 'd["id"]')
[ -n "$PAYMENT_ID" ] && pass "bypass payment $PAYMENT_ID" || { fail "bypass payment: $payment"; exit 1; }
check "simulate-success route removed" "$(status_of GET /api/v1/payments/does-not-exist/simulate-success)" 404

step "Order reaches paid (payment.succeeded consumer)"
order_status=""
for _ in $(seq 1 15); do
  order_status=$(api GET "/api/v1/orders/$ORDER_ID" "$CUSTOMER" | field 'd["status"]')
  [ "$order_status" = "paid" ] && break
  sleep 2
done
check "order status" "$order_status" paid

step "Ship (commits the reservation)"
shipped=$(api POST "/api/v1/orders/$ORDER_ID/ship" "$OWNER" \
  '{"carrier":"cj","tracking_number":"123456789012"}')
check "order status" "$(echo "$shipped" | field 'd["status"]')" in_transit
check "carrier" "$(echo "$shipped" | field 'd["carrier"]')" cj
check "tracking_number" "$(echo "$shipped" | field 'd["tracking_number"]')" 123456789012

read -r stock_after reserved_after <<<"$(stock_of "$SKU_ID")"
check "stock decremented" "$stock_after" "$((stock_before - QUANTITY))"
check "reservation released" "$reserved_after" "$reserved_before"
check "soldCount incremented" \
  "$(api GET "/api/v1/products/$PRODUCT_ID" | field 'd.get("soldCount",0)')" \
  "$((sold_before + QUANTITY))"

step "Fulfil, and reject the removed status"
check "fulfilled" "$(api PUT "/api/v1/orders/$ORDER_ID/status" "$OWNER" '{"status":"fulfilled"}' \
  | field 'd["status"]')" fulfilled
check "confirmed rejected" \
  "$(status_of PUT "/api/v1/orders/$ORDER_ID/status" "$OWNER" '{"status":"confirmed"}')" 400

printf '\n'
if [ "$failures" -eq 0 ]; then
  printf 'money path OK — order %s, product %s, customer %s\n' "$ORDER_ID" "$PRODUCT_ID" "$EMAIL"
else
  printf '%d check(s) FAILED\n' "$failures"
fi
exit $((failures > 0))
