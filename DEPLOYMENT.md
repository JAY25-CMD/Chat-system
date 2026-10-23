# Deploying the chat app (Render + Layerbase + GitHub)

## Why it worked on your PC but not online

Your local MySQL database (`chat_system`) was created by hand. The repository
never contained those tables, so the production database did not have them
either.

The old code hid that problem:

- every failed `INSERT` was reported as **"That username is already taken"**
- every failed `SELECT` / password lookup was reported as **"Invalid username
  or password"**

So a missing table, a wrong password column, or a database connection problem
all looked like an account problem. Locally the tables existed, which is why it
worked.

## What the app does now

- Creates every required table automatically on startup from `schema.sql`
  (`users`, `sessions`, `conversations`, `participants`, `messages`,
  `hidden_conversations`, `push_subscriptions`).
- Checks the schema and prints a clear error if a table is missing.
- Warns if `users.password_hash` is shorter than 60 characters (bcrypt hashes
  are 60 characters; a shorter column truncates them and breaks every login).
- Reports "username already taken" **only** for a real duplicate username
  (MySQL error 1062). Other failures are logged with the real database error.
- Logs the real database error if a login lookup fails.
- Reads `PORT`, which Render requires.
- Uses `wss://` for WebSockets on HTTPS.
- Uses secure cookies automatically behind an HTTPS proxy.
- Uses short-lived pooled connections, which avoids random failures on managed
  MySQL after idle periods.
- Works with either `DB_USER` / `DB_PASSWORD` / `DB_HOST` / `DB_PORT` /
  `DB_NAME` or a single `DATABASE_URL` / `MYSQL_URL` (`mysql://...`).
- Exposes `GET /healthz` for health checks.

## Deploy checklist

1. **Set environment variables in the Render dashboard** (not only in a local
   `.env` file, which is git-ignored):

   | Variable | Example |
   | --- | --- |
   | `DB_HOST` | Layerbase database host |
   | `DB_PORT` | `3306` (default if omitted) |
   | `DB_USER` | Layerbase database user |
   | `DB_PASSWORD` | Layerbase database password |
   | `DB_NAME` | Layerbase database name |
   | `VAPID_PUBLIC_KEY` | only if you use push notifications |
   | `VAPID_PRIVATE_KEY` | only if you use push notifications |

   Optional: `DB_TLS=true` (or `skip-verify`) if your database requires TLS.

2. **Apply the schema to the production database.** Either deploy the app (it
   creates the tables on startup when the database user has `CREATE` privilege)
   or run `schema.sql` once in the Layerbase SQL console / phpMyAdmin:

   ```
   mysql -h HOST -P PORT -u USER -p DB_NAME < schema.sql
   ```

3. **Deploy and check the Render log.** A healthy start looks like:

   ```
   connected to MySQL, database "your_db", schema ready
   server running at http://localhost:10000
   ```

   If it says `database schema error: ... missing tables`, run `schema.sql`.
   If it says `failed to connect to database`, the host/port/user/password/DB
   name or TLS setting is wrong.

4. **Test on the live site** with a brand-new username. Then log in with it.

## Other production notes

- **Attachments are stored on local disk (`uploads/`).** Render's filesystem is
  ephemeral, so files disappear after a redeploy or restart. Use an object
  store (S3, Cloudinary) or a Render persistent disk.
- **Online presence/typing is stored in memory.** Run a single Render instance,
  or use Redis, otherwise users connected to different instances will not see
  each other's typing/presence.
- **Never commit `.env`.** Keep using `.env` locally and the dashboard variables
  online.
