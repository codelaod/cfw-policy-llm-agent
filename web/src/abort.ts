/** Small local equivalent of AbortSignal.any/timeout for the browser baseline. */
export function abortScope(sources: ReadonlyArray<AbortSignal | null | undefined>, timeout = 0) {
  const controller = new AbortController()
  const removers: Array<() => void> = []
  let timer: ReturnType<typeof setTimeout> | undefined
  let disposed = false
  const dispose = () => {
    if (disposed) return
    disposed = true
    clearTimeout(timer)
    removers.splice(0).forEach(remove => remove())
  }
  for (const source of sources) {
    if (!source) continue
    if (source.aborted) { controller.abort(source.reason); dispose(); break }
    const onAbort = () => { controller.abort(source.reason); dispose() }
    source.addEventListener('abort', onAbort, { once: true })
    removers.push(() => source.removeEventListener('abort', onAbort))
  }
  if (!disposed && timeout > 0) timer = setTimeout(() => {
    controller.abort(new DOMException('Request timed out', 'TimeoutError'))
    dispose()
  }, timeout)
  return { signal: controller.signal, dispose }
}
