#!/bin/bash

# ====================================================================
# Currency Exchange Agent Demo Script
# ====================================================================
# This script demonstrates how to use the Currency Exchange Agent API
# with examples of different request types.
# ====================================================================

# Terminal colors for better readability
BLUE='\033[0;34m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Function to make API calls and format responses
call_api() {
  local request_name=$1
  local request_data=$2
  local request_id=$3
  
  echo -e "\n${BLUE}======================================================${NC}"
  echo -e "${BLUE}Example $request_id: $request_name${NC}"
  echo -e "${BLUE}======================================================${NC}"
  echo -e "${YELLOW}Request:${NC}"
  echo $request_data | jq . 2>/dev/null || echo $request_data
  echo -e "${GREEN}Response:${NC}"
  
  # Make the API call and capture the response
  response=$(curl -s -H "Content-Type: application/json" -X POST http://127.0.0.1:8081 -d "$request_data")
  
  # Pretty print the response with jq if available
  if command -v jq &> /dev/null; then
    echo $response | jq .
  else
    echo $response
  fi
  
  echo ""
  # Return the response for potential processing
  echo "$response"
}

echo -e "${GREEN}=====================================================================${NC}"
echo -e "${GREEN}                   Currency Exchange Agent Demo                      ${NC}"
echo -e "${GREEN}=====================================================================${NC}"
echo -e "This script demonstrates different ways to interact with the Currency Exchange Agent API.\n"

# Example 1: Basic currency exchange
echo -e "${YELLOW}Example 1: Basic currency exchange conversion${NC}"
echo -e "This request converts CNY to USD on a specific date.\n"

BASIC_EXCHANGE='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 1, 
  "params": {
    "id": "test-exchange-1",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "convert cny to usd on date 2025-04-16"
        }
      ]
    }
  }
}'

call_api "Basic currency exchange" "$BASIC_EXCHANGE" "1"

# Wait for processing
echo -e "${YELLOW}Waiting for the first request to complete...${NC}"
sleep 2

# Example 2: More complex exchange rate query
echo -e "${YELLOW}Example 2: More complex exchange rate query${NC}"
echo -e "This request asks for the exchange rate between EUR and JPY.\n"

COMPLEX_EXCHANGE='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 2, 
  "params": {
    "id": "test-exchange-2",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "What is the exchange rate from EUR to JPY?"
        }
      ]
    }
  }
}'

call_api "Complex exchange rate query" "$COMPLEX_EXCHANGE" "2"

# Example 3: Historical exchange rate query
echo -e "${YELLOW}Example 3: Historical exchange rate query${NC}"
echo -e "This request asks for a historical exchange rate.\n"

HISTORICAL_EXCHANGE='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 3, 
  "params": {
    "id": "test-exchange-3",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "What was the exchange rate from USD to GBP on January 1, 2023?"
        }
      ]
    }
  }
}'

call_api "Historical exchange rate query" "$HISTORICAL_EXCHANGE" "3"

echo -e "\n${GREEN}=====================================================================${NC}"
echo -e "${GREEN}                          Demo Complete                               ${NC}"
echo -e "${GREEN}=====================================================================${NC}"
echo -e "${YELLOW}Usage Tips:${NC}"
echo -e "1. Specify currency codes (USD, EUR, GBP, etc.) in your requests"
echo -e "2. Include dates for historical exchange rates"
echo -e "3. You can ask about trends or comparative rates between currencies"
echo -e "4. Run the Currency Exchange Agent with: go run main.go\n"
