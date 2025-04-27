#!/bin/bash

# ====================================================================
# Creative Writing Agent Demo Script
# ====================================================================
# This script demonstrates how to use the Creative Writing Agent API
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

# Function to make API calls and format responses
call_api() {
  local request_name=$1
  local request_data=$2
  local request_id=$3
  
  echo -e "\n${BLUE}======================================================${NC}"
  echo -e "${BLUE}Example $request_id: $request_name${NC}"
  echo -e "${BLUE}======================================================${NC}"
  echo -e "${YELLOW}Request:${NC}"
  echo $request_data | jq .
  echo -e "${GREEN}Response:${NC}"
  
  # Make the API call and capture the response
  response=$(curl -s -H "Content-Type: application/json" -X POST http://127.0.0.1:8082 -d "$request_data")
  
  # Pretty print the response with jq if available
  if command -v jq &> /dev/null; then
    echo $response | jq .
  else
    echo $response
  fi
  
  echo ""
}

echo -e "${GREEN}=====================================================================${NC}"
echo -e "${GREEN}                   Creative Writing Agent Demo                        ${NC}"
echo -e "${GREEN}=====================================================================${NC}"
echo -e "This script demonstrates different ways to interact with the Creative Writing Agent API.\n"

# Example 1: Generate a short story
echo -e "${YELLOW}Example 1: Generating a short story${NC}"
echo -e "This request asks the agent to write a short story about a robot learning emotions.\n"

STORY_REQUEST='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 1, 
  "params": {
    "id": "story-1",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "Write a short story about a robot that learns to feel emotions"
        }
      ]
    }
  }
}'

call_api "Generate a short story" "$STORY_REQUEST" "1"
STORY_ID="story-1"

# Wait for processing
echo -e "${YELLOW}Waiting for the first request to complete...${NC}"
sleep 5

# Example 2: Follow-up request in the same conversation
echo -e "${YELLOW}Example 2: Follow-up request in the same conversation${NC}"
echo -e "This request builds on the previous story by asking for a poem about the same robot.\nNote how we use the same sessionId to maintain context.\n"

POEM_REQUEST='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 2, 
  "params": {
    "id": "poem-1",
    "sessionId": "story-1",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "Now write a poem about this robot"
        }
      ]
    }
  }
}'

call_api "Follow-up poem request" "$POEM_REQUEST" "2"

# Example 3: New conversation with a different style
echo -e "${YELLOW}Example 3: New conversation with a different creative style${NC}"
echo -e "This starts a new conversation with a request for dialogue writing.\n"

DIALOGUE_REQUEST='{
  "jsonrpc": "2.0", 
  "method": "tasks/send", 
  "id": 3, 
  "params": {
    "id": "dialogue-1",
    "message": {
      "role": "user",
      "parts": [
        {
          "type": "text",
          "text": "Create a funny dialogue between a cat and a dog discussing who is better"
        }
      ]
    }
  }
}'

call_api "Dialogue writing" "$DIALOGUE_REQUEST" "3"

# Example 4: Retrieving task status
echo -e "${YELLOW}Example 4: Retrieving task status and content${NC}"
echo -e "This example shows how to retrieve a completed task by its ID.\n"

GET_REQUEST='{
  "jsonrpc": "2.0", 
  "method": "tasks/get", 
  "id": 4, 
  "params": {
    "id": "story-1"
  }
}'

call_api "Get task status" "$GET_REQUEST" "4"

echo -e "\n${GREEN}=====================================================================${NC}"
echo -e "${GREEN}                          Demo Complete                               ${NC}"
echo -e "${GREEN}=====================================================================${NC}"
echo -e "${YELLOW}Usage Tips:${NC}"
echo -e "1. Use the same sessionId for follow-up questions to maintain context"
echo -e "2. Request different creative formats: stories, poems, dialogues, etc."
echo -e "3. Retrieve task content with the tasks/get method"
echo -e "4. Run the Creative Writing Agent with: go run main.go\n"
