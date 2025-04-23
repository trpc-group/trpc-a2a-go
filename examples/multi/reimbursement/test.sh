#!/bin/bash

# ====================================================================
# Reimbursement Agent Demo Script
# ====================================================================
# This script demonstrates how to use the Reimbursement Agent API
# with examples of different request types.
# ====================================================================

# Terminal colors for better readability
BLUE='\033[0;34m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Check for required environment variable
if [ -z "$GOOGLE_API_KEY" ]; then
  echo -e "${RED}Error: GOOGLE_API_KEY environment variable is not set.${NC}"
  echo -e "Please set it with: ${YELLOW}export GOOGLE_API_KEY=your_api_key${NC}"
  exit 1
fi

# Function to extract request_id from JSON response
extract_request_id() {
  local json="$1"
  # Remove newlines and handle escaped quotes for better matching
  local cleaned_json=$(echo "$json" | tr -d '\n' | sed 's/\\"/"/g')
  # Try multiple patterns to increase chances of matching
  local request_id=$(echo "$cleaned_json" | grep -o '"request_id": *"request_id_[0-9]*"' | grep -o 'request_id_[0-9]*' | head -1)
  
  if [ -z "$request_id" ]; then
    # Try alternate pattern with different quote formatting
    request_id=$(echo "$cleaned_json" | grep -o '"request_id":"request_id_[0-9]*"' | grep -o 'request_id_[0-9]*' | head -1)
  fi
  
  echo "$request_id"
}

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
  response=$(curl -s -H "Content-Type: application/json" -X POST http://127.0.0.1:8083 -d "$request_data")
  
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
echo -e "${GREEN}                   Reimbursement Agent Demo                          ${NC}"
echo -e "${GREEN}=====================================================================${NC}"
echo -e "This script demonstrates different ways to interact with the Reimbursement Agent API.\n"

# Example 1: New reimbursement request
echo -e "${YELLOW}Example 1: Creating a new reimbursement request${NC}"
echo -e "This request initiates a new reimbursement process and returns a form.\n"

NEW_REQUEST='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 1, 
  "params": {
    "id": "test-reimburse-1",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "I need to get reimbursed for $50 spent on office supplies on 2023-11-15."
        }
      ]
    }
  }
}'

RESPONSE=$(call_api "Create reimbursement request" "$NEW_REQUEST" "1")

# Extract request_id using the function
REQUEST_ID=$(extract_request_id "$RESPONSE")
if [ -z "$REQUEST_ID" ]; then
  echo -e "${YELLOW}Couldn't extract request_id from response. Using fallback ID.${NC}"
  # Use a fallback ID since we can't extract it
  REQUEST_ID="request_id_$(date +%s)"
else
  echo -e "${GREEN}Extracted request_id: $REQUEST_ID${NC}"
fi

# Wait for processing
echo -e "${YELLOW}Waiting for the first request to complete...${NC}"
sleep 2

# Example 2: Form submission with extracted request_id
echo -e "${YELLOW}Example 2: Submitting a reimbursement form${NC}"
echo -e "This request submits the completed form with the request_id from the first response.\n"

FORM_SUBMIT='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 2, 
  "params": {
    "id": "test-reimburse-2",
    "sessionId": "test-reimburse-1",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "{\"request_id\":\"'"$REQUEST_ID"'\",\"date\":\"2023-11-15\",\"amount\":\"$50\",\"purpose\":\"Office supplies including pens, notebooks, and printer paper\"}"
        }
      ]
    }
  }
}'

call_api "Submit reimbursement form" "$FORM_SUBMIT" "2"

# Example 3: Incomplete form submission
echo -e "${YELLOW}Example 3: Starting a new request with incomplete information${NC}"
echo -e "This demonstrates how the agent handles incomplete information.\n"

INCOMPLETE_REQUEST='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 3, 
  "params": {
    "id": "test-reimburse-3",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "I need to get reimbursed for some expenses."
        }
      ]
    }
  }
}'

RESPONSE2=$(call_api "Incomplete reimbursement request" "$INCOMPLETE_REQUEST" "3")

# Extract request_id from the second response
REQUEST_ID2=$(extract_request_id "$RESPONSE2")
if [ -z "$REQUEST_ID2" ]; then
  echo -e "${YELLOW}Couldn't extract request_id from response. Using fallback ID.${NC}"
  # Use a fallback ID since we can't extract it
  REQUEST_ID2="request_id_$(date +%s)"
else
  echo -e "${GREEN}Extracted request_id: $REQUEST_ID2${NC}"
fi

# Wait for processing
echo -e "${YELLOW}Waiting for the request to complete...${NC}"
sleep 2

# Example 4: Plain text form submission
echo -e "${YELLOW}Example 4: Submitting a form in plain text format${NC}"
echo -e "This demonstrates the agent's ability to parse form data from plain text.\n"

PLAIN_TEXT_SUBMIT='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 4, 
  "params": {
    "id": "test-reimburse-4",
    "sessionId": "test-reimburse-3",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "request_id: '"$REQUEST_ID2"'\ndate: 2023-12-01\namount: $75.50\npurpose: Team lunch meeting"
        }
      ]
    }
  }
}'

call_api "Plain text form submission" "$PLAIN_TEXT_SUBMIT" "4"

echo -e "\n${GREEN}=====================================================================${NC}"
echo -e "${GREEN}                          Demo Complete                               ${NC}"
echo -e "${GREEN}=====================================================================${NC}"
echo -e "${YELLOW}Usage Tips:${NC}"
echo -e "1. Use the same sessionId for follow-up requests to maintain context"
echo -e "2. The agent will generate a request_id which must be included in form submissions"
echo -e "3. Form data can be submitted in either JSON or plain text format"
echo -e "4. Run the Reimbursement Agent with: go run main.go\n"
