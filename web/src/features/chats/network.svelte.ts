export const network = $state({ online: navigator.onLine });

addEventListener('online', () => {
  network.online = true;
});
addEventListener('offline', () => {
  network.online = false;
});
