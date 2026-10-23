export type NodeKind = 'directory' | 'file'

export interface Tenant {
  display_name: string
  root_directory_id: string
}

export interface Node {
  id: string
  parent_id?: string
  kind: NodeKind
  name: string
  size?: number
  mime_type?: string
  revision: number
}

export interface Breadcrumb {
  id: string
  name: string
}

export interface RecycleEntry {
  node: Node
  original_parent_id: string
  deleted_at: string
}

export interface PageResponse<T> {
  data: T[]
  page: { next_cursor: string | null }
}

export interface UploadSession {
  id: string
  status: string
  part_size: number
  expected_size: number
}

export interface SignedPart {
  part_number: number
  method: string
  url: string
  required_headers?: Record<string, string>
}

export interface CompletedPart {
  part_number: number
  etag: string
  size: number
}

export type Notice = { kind: 'success' | 'error'; text: string } | null
