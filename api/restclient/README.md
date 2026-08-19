# REST requests for local testing

Place these `.http` files in the `api/restclient` folder. They target the local API at `http://localhost:8080`.

Quick start:

1. Run the API (see project README or `make run`).
2. In VS Code, open any `.http` file and use the REST Client extension to send requests.

Notes:

- Seeded users: alice..eve use UUIDs ending in `...0001`..`...0005`.
- Replace `{{order_id}}` placeholders with real IDs returned from place-order responses.
