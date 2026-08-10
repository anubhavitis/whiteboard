import type { ConnectionStatus } from '../agent/protocol'

const COLORS: Record<ConnectionStatus, string> = {
  connecting: '#e0a800',
  connected: '#1f9d55',
  disconnected: '#d64545',
}

export function ConnectionDot({ status }: { status: ConnectionStatus }) {
  return (
    <div className="connection-dot" title={`agent: ${status}`}>
      <span className="connection-dot__light" style={{ background: COLORS[status] }} />
      {status}
    </div>
  )
}
