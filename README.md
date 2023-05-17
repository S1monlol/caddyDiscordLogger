docker run -d -v /var/log/caddy:/var/log/caddy/ caddy


docker run -d -p 80:80 -p 443:443 -v ./Caddyfile:/etc/caddy/Caddyfile -v /var/log/caddy:/var/log/caddy/ -v caddy_data:/data caddy