self.addEventListener("push", function (event) {
  var data = event.data ? event.data.json() : {};

  var title = data.title || "New message";
  var options = {
    body: data.body || "",
    icon: "/static/icon.png",
    data: { url: data.url || "/dashboard" },
  };

  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener("notificationclick", function (event) {
  event.notification.close();
  event.waitUntil(clients.openWindow(event.notification.data.url));
});
