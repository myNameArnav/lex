// streamError describes a failed media request from its status and body:
// Lex's own JSON { error } explains it (serverError), anything else (a
// proxy's page) only has the status.
export function streamError(status, body) {
  try {
    const j = JSON.parse(body);
    if (j?.error) return { msg: j.error, serverError: true };
  } catch {}
  return { msg: `Stream failed (HTTP ${status})`, serverError: false };
}
