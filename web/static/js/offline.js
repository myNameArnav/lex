// The offline page (served by sw.js when the server is unreachable): Retry
// reloads the page, which keeps the hash route; coming back online retries too.
const retry = () => location.reload();
document.getElementById('retry')?.addEventListener('click', retry);
window.addEventListener('online', retry);
