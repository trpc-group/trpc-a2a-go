#!/usr/bin/env python3
"""
Official A2A Python SDK Client Example

This example demonstrates how to use the official A2A Python SDK
to communicate with the Simple Text Reversal A2A Go server.

Usage:
    python official_a2a_client.py --message "Hello World"
    python official_a2a_client.py --mode test
    python official_a2a_client.py --mode streaming
    python official_a2a_client.py --mode info
"""

import asyncio
import argparse
import json
import sys
import uuid

try:
    from a2a.client import A2AClient, create_text_message_object
    from a2a.types import SendMessageRequest, SendStreamingMessageRequest, MessageSendParams, Role, MessageSendConfiguration
    import httpx
    print("✅ Successfully imported official A2A SDK")
except ImportError as e:
    print(f"❌ Failed to import A2A SDK: {e}")
    print("Please install: pip install a2a-sdk httpx")
    sys.exit(1)


class SimpleA2AClient:
    """Simple client for the Text Reversal A2A server"""
    
    def __init__(self, server_url: str = "http://localhost:8080"):
        self.server_url = server_url
        self.client = None
        self.httpx_client = None
        
    async def connect(self):
        """Connect to the A2A server"""
        try:
            self.httpx_client = httpx.AsyncClient()
            self.client = A2AClient(httpx_client=self.httpx_client, url=self.server_url)
            print(f"🔗 Connected to: {self.server_url}")
            return True
        except Exception as e:
            print(f"❌ Connection failed: {e}")
            return False
    
    async def close(self):
        """Close the HTTP client"""
        if self.httpx_client:
            await self.httpx_client.aclose()
    
    async def send_message(self, text: str) -> str:
        """Send a text message and get the reversed response"""
        if not self.client:
            raise Exception("Not connected to server")
            
        # Create A2A message
        message = create_text_message_object(role=Role.user, content=text)
        
        # Create request
        params = MessageSendParams(message=message)
        request = SendMessageRequest(
            id=str(uuid.uuid4()),
            jsonrpc="2.0",
            method="message/send",
            params=params
        )
        
        # Send request
        response = await self.client.send_message(request)
        
        # Extract response text
        response_data = response.model_dump()
        result = response_data.get('result', {})
        parts = result.get('parts', [])
        
        if parts and 'text' in parts[0]:
            return parts[0]['text']
        else:
            raise Exception("Invalid response format")
    
    async def send_streaming_message(self, text: str):
        """Send a streaming message and handle streaming response"""
        if not self.client:
            raise Exception("Not connected to server")
            
        print(f"📤 Sending streaming message: '{text}'")
        
        try:
            # Create A2A message for streaming
            message = create_text_message_object(role=Role.user, content=text)
            params = MessageSendParams(message=message, configuration=MessageSendConfiguration(acceptedOutputModes=["text"]))
            
            request = SendStreamingMessageRequest(
                id=str(uuid.uuid4()),
                jsonrpc="2.0",
                method="message/stream",
                params=params
            )
            
            chunk_count = 0
            final_result = ""
            
            # Process streaming events: working status -> completed status + message -> artifact
            async for chunk in self.client.send_message_streaming(request):
                chunk_count += 1
                chunk_data = chunk.model_dump() if hasattr(chunk, 'model_dump') else chunk
                
                if isinstance(chunk_data, dict) and 'result' in chunk_data:
                    result = chunk_data['result']
                    if isinstance(result, dict):
                        kind = result.get("kind", "")
                        
                        if kind == "status-update":
                            status = result.get("status", {})
                            state = str(status.get("state", "")).replace("TaskState.", "")
                            print(f"📥 Status: {state}")
                            
                            # Extract final message from completed status
                            if "completed" in state and "message" in status and status["message"]:
                                message = status["message"]
                                parts = message.get("parts", [])
                                if parts and "text" in parts[0]:
                                    final_result = parts[0]["text"]
                                    print(f"   💬 Result: {final_result}")
                                    
                        elif kind == "artifact-update":
                            artifact = result.get("artifact", {})
                            name = artifact.get("name", "Artifact")
                            parts = artifact.get("parts", [])
                            if parts and "text" in parts[0]:
                                text_content = parts[0]["text"]
                                print(f"📥 {name}: {text_content}")
            
            print(f"✅ Received {chunk_count} streaming chunks")
            if final_result:
                print(f"📝 Final result: '{final_result}'")
                
        except Exception as e:
            print(f"⚠️  Streaming failed, falling back to regular message: {e}")
            # Fallback to regular message
            response = await self.send_message(text)
            print(f"📥 Fallback response: '{response}'")
    
    async def get_agent_info(self):
        """Get agent card information"""
        if not self.httpx_client:
            raise Exception("Not connected")
            
        response = await self.httpx_client.get(f"{self.server_url}/.well-known/agent.json")
        if response.status_code == 200:
            return response.json()
        else:
            raise Exception(f"Failed to get agent info: {response.status_code}")


async def run_single_message(client, message):
    """Send a single message"""
    print(f"📤 Sending: '{message}'")
    try:
        response = await client.send_message(message)
        print(f"📥 Received: '{response}'")
    except Exception as e:
        print(f"❌ Error: {e}")


async def run_test_suite(client):
    """Run basic functionality tests"""
    print("\n🧪 Running test suite...")
    
    test_messages = [
        "Hello World",
        "Python A2A SDK",
        "12345",
        "Hello, 世界!",
        "The quick brown fox"
    ]
    
    passed = 0
    for i, msg in enumerate(test_messages, 1):
        print(f"\n--- Test {i}/{len(test_messages)} ---")
        try:
            print(f"📤 Input: '{msg}'")
            response = await client.send_message(msg)
            print(f"📥 Output: '{response}'")
            
            # Verify it's actually reversed
            expected = msg[::-1]  # Python string reversal
            if response.startswith("Processed result: "):
                actual_reversed = response.replace("Processed result: ", "")
                if actual_reversed == expected:
                    print("✅ Text reversal verified!")
                    passed += 1
                else:
                    print(f"⚠️  Expected: '{expected}', got: '{actual_reversed}'")
            else:
                print("⚠️  Unexpected response format")
                
        except Exception as e:
            print(f"❌ Test failed: {e}")
        
        await asyncio.sleep(0.3)  # Small delay
    
    print(f"\n📊 Results: {passed}/{len(test_messages)} tests passed")


async def run_streaming_test(client):
    """Test streaming functionality"""
    print("\n🌊 Testing streaming functionality...")
    
    streaming_messages = [
        "Stream test: Hello World",
        "Generate a longer response about text processing",
        "Tell me about streaming in A2A protocol"
    ]
    
    for i, msg in enumerate(streaming_messages, 1):
        print(f"\n--- Streaming Test {i}/{len(streaming_messages)} ---")
        try:
            await client.send_streaming_message(msg)
        except Exception as e:
            print(f"❌ Streaming test {i} failed: {e}")
        
        await asyncio.sleep(0.5)  # Small delay between tests


async def show_agent_info(client):
    """Display agent card information"""
    try:
        info = await client.get_agent_info()
        print("📋 Agent Information:")
        print(json.dumps(info, indent=2))
    except Exception as e:
        print(f"❌ Failed to get agent info: {e}")


async def main():
    """Main function"""
    parser = argparse.ArgumentParser(description="Simple A2A Text Reversal Client")
    parser.add_argument("--server", default="http://localhost:8080", 
                       help="Server URL (default: http://localhost:8080)")
    parser.add_argument("--mode", choices=["test", "streaming", "info"], 
                       default="test", help="Run mode")
    parser.add_argument("--message", help="Single message to send")
    
    args = parser.parse_args()
    
    print("🐍 Simple A2A Text Reversal Client")
    print("=" * 40)
    
    # Create client
    client = SimpleA2AClient(args.server)
    
    try:
        # Connect
        if not await client.connect():
            print("❌ Failed to connect. Make sure the server is running:")
            print("   cd examples/simple/server && go run main.go")
            return 1
        
        # Run based on mode
        if args.mode == "streaming":
            if args.message:
                # Single streaming message
                await client.send_streaming_message(args.message)
            else:
                # Streaming test suite
                await run_streaming_test(client)
        elif args.mode == "info":
            await show_agent_info(client)
        elif args.message:
            # Single regular message
            await run_single_message(client, args.message)
        else:  # test mode
            await run_test_suite(client)
            
    except KeyboardInterrupt:
        print("\n👋 Interrupted by user")
    except Exception as e:
        print(f"❌ Error: {e}")
        return 1
    finally:
        await client.close()
    
    print("\n✅ Client completed")
    return 0


if __name__ == "__main__":
    exit_code = asyncio.run(main())
    sys.exit(exit_code) 