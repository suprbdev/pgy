# pgy example schemas

A library of ready-made schema modules for rapidly prototyping new projects.
Pick the modules you need, add the links that connect them, and run one
command to get a working database.

```sh
pgy diff --from-empty \
  --schemas examples/modules/core/user.yml,examples/modules/content/post.yml,examples/links/post_author.yml
pgy commit -m "initial schema"
pgy migrate
```

## Layout

- **`modules/`** — self-contained building blocks. Every module works on its
  own: it declares its tables, enum types, sequences, triggers, and helper
  functions, and never references a table from another module.
- **`links/`** — supplementary configs that connect two modules. A link either
  adds a foreign-key column to one module's table (e.g. `post_author.yml`
  gives `post` an `author_id` referencing `user`) or declares a junction
  table (e.g. `post_tag.yml`). Only include a link when both of its modules
  are included.

All tables use singular names (`user`, `post`, `order`, ...). Reserved words
are safe: pgy quotes all identifiers. Primary keys are `uuid` defaulting to
`gen_random_uuid()` (built into PostgreSQL 13+), money is stored in minor
units (`*_cents bigint`), and timestamps are `timestamptz`. Modules with an
`updated_at` column ship the shared `set_updated_at()` trigger function —
declaring it in several selected files is fine, they merge into one.

## Modules

| Module | Tables | What it gives you |
|--------|--------|-------------------|
| `core/user.yml` | `user` | accounts, email validation, status enum |
| `core/organization.yml` | `organization` | teams / workspaces / tenants |
| `core/session.yml` | `session` | server-side auth sessions |
| `core/api_key.yml` | `api_key` | hashed API keys with scopes |
| `core/file.yml` | `file` | upload metadata for object storage |
| `core/notification.yml` | `notification` | in-app notifications |
| `core/audit_log.yml` | `audit_log` | append-only audit trail |
| `core/tag.yml` | `tag` | free-form labels |
| `core/setting.yml` | `setting` | key/value app settings |
| `core/address.yml` | `address` | postal addresses |
| `core/webhook.yml` | `webhook`, `webhook_delivery` | outbound webhooks with retries |
| `core/feature_flag.yml` | `feature_flag` | runtime toggles |
| `core/job.yml` | `job` | DB-backed job queue (SKIP LOCKED) |
| `content/post.yml` | `post` | blog / CMS articles |
| `content/comment.yml` | `comment` | threaded comments |
| `content/category.yml` | `category` | hierarchical categories |
| `commerce/product.yml` | `product` | catalog with prices |
| `commerce/order.yml` | `order`, `order_item` | orders with line items + number sequence |
| `commerce/cart.yml` | `cart`, `cart_item` | shopping carts |
| `commerce/payment.yml` | `payment` | provider-agnostic payments |
| `commerce/inventory.yml` | `warehouse`, `stock` | stock levels |
| `commerce/coupon.yml` | `coupon` | discount codes |
| `commerce/review.yml` | `review` | 1–5 star reviews |
| `billing/plan.yml` | `plan` | subscription pricing plans |
| `billing/subscription.yml` | `subscription` | recurring subscriptions |
| `billing/invoice.yml` | `invoice`, `invoice_line` | invoices + number sequence |
| `messaging/conversation.yml` | `conversation`, `message` | chat threads |
| `crm/company.yml` | `company` | CRM accounts |
| `crm/contact.yml` | `contact` | CRM people |
| `crm/deal.yml` | `deal` | sales pipeline |
| `scheduling/event.yml` | `event` | calendar events / slots |
| `scheduling/booking.yml` | `booking` | reservations |
| `support/ticket.yml` | `ticket` | helpdesk tickets |
| `analytics/activity.yml` | `activity` (+partitions) | partitioned event stream |

## Links

FK links add a column + foreign key to a module's table; junction links add a
new table. Header comments in each file list the required modules.

| Link | Connects | Shape |
|------|----------|-------|
| `membership.yml` | user ↔ organization | junction + role |
| `organization_owner.yml` | organization → user | owner FK |
| `session_user.yml` | session → user | FK |
| `api_key_user.yml` | api_key → user | FK |
| `notification_user.yml` | notification → user | recipient FK |
| `file_user.yml` | file → user | uploader FK |
| `address_user.yml` | address → user | FK |
| `audit_log_user.yml` | audit_log → user | actor FK |
| `post_author.yml` | post → user | author FK |
| `comment_post.yml` | comment → post | FK |
| `comment_author.yml` | comment → user | author FK |
| `post_tag.yml` | post ↔ tag | junction |
| `post_category.yml` | post → category | FK |
| `product_category.yml` | product → category | FK |
| `product_tag.yml` | product ↔ tag | junction |
| `order_customer.yml` | order → user | FK (nullable: guest checkout) |
| `order_product.yml` | order_item → product | FK (nullable: keeps history) |
| `order_address.yml` | order → address | shipping + billing FKs |
| `cart_user.yml` | cart → user | FK |
| `cart_product.yml` | cart_item → product | FK |
| `payment_order.yml` | payment → order | FK |
| `payment_invoice.yml` | payment → invoice | FK |
| `review_product.yml` | review → product | FK |
| `review_author.yml` | review → user | FK |
| `stock_product.yml` | stock → product | FK + (warehouse, product) unique |
| `subscription_user.yml` | subscription → user | FK |
| `subscription_plan.yml` | subscription → plan | FK |
| `invoice_user.yml` | invoice → user | FK |
| `invoice_subscription.yml` | invoice → subscription | FK |
| `participant.yml` | user ↔ conversation | junction |
| `message_sender.yml` | message → user | FK |
| `contact_company.yml` | contact → company | FK |
| `deal_company.yml` | deal → company | FK |
| `deal_contact.yml` | deal → contact | FK |
| `deal_owner.yml` | deal → user | FK |
| `booking_event.yml` | booking → event | FK |
| `booking_user.yml` | booking → user | FK |
| `event_organizer.yml` | event → user | FK |
| `ticket_user.yml` | ticket → user | requester + assignee FKs |

## Recipes

Blog:

```sh
--schemas examples/modules/core/user.yml,examples/modules/content/post.yml,examples/modules/content/comment.yml,examples/modules/core/tag.yml,examples/links/post_author.yml,examples/links/comment_post.yml,examples/links/comment_author.yml,examples/links/post_tag.yml
```

SaaS starter:

```sh
--schemas examples/modules/core/user.yml,examples/modules/core/organization.yml,examples/modules/core/session.yml,examples/modules/core/api_key.yml,examples/modules/billing/plan.yml,examples/modules/billing/subscription.yml,examples/modules/billing/invoice.yml,examples/links/membership.yml,examples/links/session_user.yml,examples/links/api_key_user.yml,examples/links/subscription_user.yml,examples/links/subscription_plan.yml,examples/links/invoice_user.yml,examples/links/invoice_subscription.yml
```

E-commerce:

```sh
--schemas examples/modules/core/user.yml,examples/modules/core/address.yml,examples/modules/commerce/product.yml,examples/modules/commerce/order.yml,examples/modules/commerce/cart.yml,examples/modules/commerce/payment.yml,examples/links/order_customer.yml,examples/links/order_product.yml,examples/links/order_address.yml,examples/links/address_user.yml,examples/links/cart_user.yml,examples/links/cart_product.yml,examples/links/payment_order.yml
```

Marketplace: e-commerce + `commerce/review.yml`, `commerce/inventory.yml`,
`links/review_product.yml`, `links/review_author.yml`, `links/stock_product.yml`.

CRM: `crm/company.yml`, `crm/contact.yml`, `crm/deal.yml`, `core/user.yml` +
`links/contact_company.yml`, `links/deal_company.yml`, `links/deal_contact.yml`,
`links/deal_owner.yml`.

Community/forum: user + post + comment + tag + notification + the matching links.

Helpdesk: `support/ticket.yml`, `core/user.yml` + `links/ticket_user.yml`.

Booking platform: `scheduling/event.yml`, `scheduling/booking.yml`,
`core/user.yml` + `links/booking_event.yml`, `links/booking_user.yml`,
`links/event_organizer.yml`.

## How links work

`pgy` merges all `--schemas` files before diffing. When the same table appears
in more than one file, its columns, indexes, foreign keys, constraints,
triggers, and `dependsOn` entries are combined (same-named entries from later
files replace earlier ones). A link file exploits this: it re-declares the
module's table with only the extra column and foreign key, so the module file
never has to know about the link.

`schema.yml` in this directory is the original kitchen-sink example kept for
reference; the `modules/` + `links/` files are the recommended starting point.
