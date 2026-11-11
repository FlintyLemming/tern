// FakeES stands in for EventSource: tests push events by hand.
export class FakeES {
  static all: FakeES[] = []
  readyState = 0
  url: string
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  listeners = new Map<string, ((e: MessageEvent) => void)[]>()
  closed = false
  constructor(url: string) {
    this.url = url
    FakeES.all.push(this)
  }
  addEventListener(type: string, fn: (e: MessageEvent) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn])
  }
  close() {
    this.closed = true
    this.readyState = 2
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) fn(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
  static make = (url: string) => new FakeES(url) as unknown as EventSource
}
