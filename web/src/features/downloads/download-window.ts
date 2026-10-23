/**
 * Open the download target synchronously from the click handler. A window opened after
 * an awaited authorization request is commonly blocked as an unsolicited popup.
 */
export interface DownloadWindow {
  readonly closed: boolean
  setUrl(url: string): void
  close(): void
}

export function openDownloadWindow(): DownloadWindow | null {
  const popup = window.open('about:blank', '_blank')
  if (!popup) return null
  popup.opener = null
  return {
    get closed() { return popup.closed },
    setUrl(url: string) {
      if (!popup.closed) popup.location.replace(url)
    },
    close() {
      if (!popup.closed) popup.close()
    },
  }
}
