import { useEffect, useRef, useState } from 'react'
import type { ClientMessage, ConnectionStatus, ServerMessage } from './protocol'

const WS_URL = import.meta.env.VITE_WS_URL ?? 'ws://localhost:8787/ws'

interface Options {
  onMessage?: (msg: ServerMessage) => void
}

/**
 * Owns the single session WebSocket (D4). Phase 0 connects and exposes status;
 * reconnect-with-backoff is Phase 5.1.
 */
export function useAgentSocket({ onMessage }: Options = {}) {
  const [status, setStatus] = useState<ConnectionStatus>('connecting')
  const socketRef = useRef<WebSocket | null>(null)

  // Kept in a ref so a changing callback identity never re-opens the socket.
  const handlerRef = useRef(onMessage)
  handlerRef.current = onMessage

  useEffect(() => {
    const socket = new WebSocket(WS_URL)
    socketRef.current = socket

    socket.onopen = () => setStatus('connected')
    socket.onclose = () => setStatus('disconnected')
    socket.onerror = () => setStatus('disconnected')
    socket.onmessage = (event) => {
      try {
        handlerRef.current?.(JSON.parse(event.data) as ServerMessage)
      } catch {
        console.error('unparseable frame from server', event.data)
      }
    }

    return () => {
      socketRef.current = null
      socket.close()
    }
  }, [])

  function send(message: ClientMessage): boolean {
    const socket = socketRef.current
    if (socket?.readyState !== WebSocket.OPEN) return false
    socket.send(JSON.stringify(message))
    return true
  }

  return { status, send }
}
