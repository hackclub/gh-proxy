#!/bin/bash

# Test script to validate cumulative stats functionality
# This script will make multiple requests and verify that stats accumulate correctly

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${YELLOW}Starting cumulative stats test...${NC}"

# Get base URL and admin credentials from environment or use defaults
BASE_URL="${BASE_URL:-http://localhost:8080}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-admin}"

# Function to make authenticated admin requests
admin_curl() {
    curl -s -u "$ADMIN_USER:$ADMIN_PASS" "$@"
}

# Function to extract stat value from admin page
get_admin_stat() {
    local stat_name="$1"
    admin_curl "$BASE_URL/admin" | grep -o "data-${stat_name}=\"[^\"]*\"" | sed "s/data-${stat_name}=\"//;s/\"//"
}

# Function to get JSON endpoint stats
get_json_stat() {
    local endpoint="$1"
    local field="$2"
    admin_curl "$BASE_URL$endpoint" | python3 -c "import json,sys; data=json.load(sys.stdin); print(data[0]['$field'] if isinstance(data, list) and len(data) > 0 else data.get('$field', 0))" 2>/dev/null || echo "0"
}

echo -e "${YELLOW}Step 1: Getting initial stats...${NC}"

# Get initial stats (should be 0 or low after fresh migration)
initial_total=$(get_admin_stat "total-requests" || echo "0")
initial_cache_rate=$(get_admin_stat "cache-hit-rate" || echo "0.0%")
initial_today=$(get_admin_stat "today-requests" || echo "0")

echo "Initial stats:"
echo "  Total Requests: $initial_total"
echo "  Cache Hit Rate: $initial_cache_rate" 
echo "  Today's Requests: $initial_today"

echo -e "${YELLOW}Step 2: Creating test API key...${NC}"

# Create a test API key (this will fail if server isn't running)
# We'll try to get CSRF token first
csrf_token=$(admin_curl "$BASE_URL/admin" | grep -o 'name="csrf" value="[^"]*"' | sed 's/name="csrf" value="//;s/"//' || echo "")

if [ -z "$csrf_token" ]; then
    echo -e "${RED}Error: Could not get CSRF token. Is the server running at $BASE_URL?${NC}"
    exit 1
fi

echo "Got CSRF token: ${csrf_token:0:10}..."

# Create API key for testing
test_key=$(admin_curl -X POST "$BASE_URL/admin/apikeys" \
    -d "hc_username=test" \
    -d "app_name=stats-test" \
    -d "machine=test-machine" \
    -d "rate_limit=100" \
    -d "csrf=$csrf_token" | grep -o 'test_stats-test_test-machine_[a-z0-9]*' | head -1)

if [ -z "$test_key" ]; then
    echo -e "${RED}Error: Could not create test API key${NC}"
    exit 1
fi

echo "Created test API key: ${test_key:0:20}..."

echo -e "${YELLOW}Step 3: Making test requests...${NC}"

# Make several requests to test stats accumulation
for i in {1..5}; do
    echo "Making request $i/5..."
    
    # Make a request that should be cacheable (GET)
    response=$(curl -s -H "X-API-Key: $test_key" \
        "$BASE_URL/gh/user" || echo "error")
    
    if [[ "$response" == *"error"* ]] || [ -z "$response" ]; then
        echo -e "${RED}Warning: Request $i failed or returned empty response${NC}"
    fi
    
    # Small delay to ensure requests are processed
    sleep 0.2
done

echo -e "${YELLOW}Step 4: Checking updated stats...${NC}"

# Wait a moment for stats to be processed
sleep 2

# Get updated stats
new_total=$(get_admin_stat "total-requests" || echo "0")
new_cache_rate=$(get_admin_stat "cache-hit-rate" || echo "0.0%")
new_today=$(get_admin_stat "today-requests" || echo "0")

echo "Updated stats:"
echo "  Total Requests: $new_total"
echo "  Cache Hit Rate: $new_cache_rate"
echo "  Today's Requests: $new_today"

echo -e "${YELLOW}Step 5: Checking per-API-key stats...${NC}"

# Check API key specific stats
key_stats=$(admin_curl "$BASE_URL/admin/keys.json" | python3 -c "
import json, sys
data = json.load(sys.stdin)
for key in data:
    if 'test_stats-test_test-machine' in key.get('display', ''):
        print(f\"Key: {key['display']}\")
        print(f\"Total Requests: {key['total']}\")
        print(f\"Hit Rate: {key['hit_rate']:.1f}%\")
        break
else:
    print('Test key not found')
" 2>/dev/null || echo "Error getting key stats")

echo "Per-API-Key Stats:"
echo "$key_stats"

echo -e "${YELLOW}Step 6: Validation...${NC}"

# Basic validation
total_diff=$((new_total - initial_total))
today_diff=$((new_today - initial_today))

echo "Analysis:"
echo "  Total requests increased by: $total_diff"
echo "  Today's requests increased by: $today_diff"

if [ "$total_diff" -gt 0 ] && [ "$today_diff" -gt 0 ]; then
    echo -e "${GREEN}✓ Stats are accumulating correctly!${NC}"
else
    echo -e "${RED}✗ Stats may not be accumulating properly${NC}"
fi

echo -e "${YELLOW}Step 7: Testing admin page direct access...${NC}"

# Test direct admin page access
admin_page=$(admin_curl "$BASE_URL/admin" | grep -c "Dashboard" || echo "0")

if [ "$admin_page" -gt 0 ]; then
    echo -e "${GREEN}✓ Admin page accessible${NC}"
else
    echo -e "${RED}✗ Admin page not accessible${NC}"
fi

echo -e "${YELLOW}Test completed!${NC}"
echo
echo -e "${YELLOW}To test WebSocket functionality:${NC}"
echo "1. Open $BASE_URL/admin in a browser"
echo "2. Make requests using: curl -H \"X-API-Key: $test_key\" \"$BASE_URL/gh/user\""
echo "3. Watch the stats update in real-time on the admin dashboard"
