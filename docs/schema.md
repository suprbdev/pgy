# Schema YAML Format Documentation

This document describes the structure and format of the YAML schema files used in this project.

## Overview

Schema files define the structure and validation rules for database objects including tables, columns, types, functions, and extensions. They use a hierarchical YAML format with specific conventions for defining these elements.

## Basic Structure

```yaml
# Schema definition
extensions:
  - name: <extension_name>
    ifNotExists: <true/false>

schemas:
  <schema_name>:
    # Table definitions
    tables:
      <table_name>:
        columns:
          <column_name>:
            type: <data_type>
            # Column properties...
    
    # Type definitions  
    types:
      <type_name>:
        type: <enum|composite>
        # Type-specific properties...
    
    # Function definitions
    functions:
      <function_name>(<args>): 
        returns: <return_type>
        language: <language>
        # Function properties...
```

## Schema Structure

### Extensions
```yaml
extensions:
  - name: <extension_name>
    ifNotExists: <true/false>
    dependsOn:
      - <dependency>
```

### Schemas and Tables
```yaml
schemas:
  <schema_name>:
    tables:
      <table_name>:
        columns:
          <column_name>:
            type: <data_type>
            nullable: <true/false>
            notNull: <true/false>
            default: <default_value>
            unique: <true/false>
            primaryKey: <true/false>
        primaryKey:
          <constraint_name>:
            columns: [<column1>, <column2>]
        indexes:
          <index_name>:
            columns: [<column1>, <column2>]
            unique: <true/false>
        foreignKeys:
          <constraint_name>:
            columns: [<column1>, <column2>]
            references:
              table: <referenced_table>
              columns: [<column1>, <column2>]
            onDelete: <cascade|restrict|set null|set default>
        triggers:
          <trigger_name>:
            timing: <before/after>
            events: [<insert/update/delete>]
            level: <row/statement>
            procedure: <procedure_name>
        constraints:
          <constraint_name>:
            type: <check/unique/exclude>
            expression: <expression> # Used for check or exclude
            columns: [<column1>]     # Used for unique
        dependsOn:
          - <dependency>
```

## Field Types

### Primitive Types
- `text` - Text values
- `integer` - Whole numbers  
- `bigint` - Large integers
- `numeric` - Precise decimal numbers
- `boolean` - True/false values
- `date` - Date values (ISO 8601 format)
- `timestamptz` - Timestamp with timezone
- `uuid` - Universally unique identifier
- `jsonb` - JSON binary data
- `citext` - Case-insensitive text

### Complex Types
- `enum` - Enumeration type with predefined labels
- `composite` - Composite type with named attributes

## Column Properties

Each column can have the following properties:

| Property | Type | Description |
|----------|------|-------------|
| `type` | string | Data type of the column |
| `nullable` | boolean | Whether the column allows NULL values |
| `notNull` | boolean | Whether the column requires a value (opposite of nullable) |
| `default` | string | Default value if not provided |
| `unique` | boolean | Whether the column values must be unique |
| `primaryKey` | boolean | Whether the column is part of the primary key |

## Type Definitions

### Enum Types
```yaml
types:
  <type_name>:
    type: enum
    labels:
      - <label1>
      - <label2>
```

### Composite Types
```yaml
types:
  <type_name>:
    type: composite
    attributes:
      <attribute_name>:
        type: <data_type>
    dependsOn:
      - <dependency>
```

## Function Definitions

```yaml
functions:
  <function_name>(<args>): 
    returns: <return_type>
    language: <language>
    security: <definer/invoker>
    volatility: <stable/volatile/immutable>
    strict: <true/false>
    set:
      <option>: <value>
    body: |
      <function_body>
    dependsOn:
      - <dependency>
```

## Example Schema

```yaml
extensions:
  - name: pgcrypto
    ifNotExists: true
  - name: uuid-ossp
    ifNotExists: true
  - name: citext
    ifNotExists: true

schemas:
  public:
    types:
      auth_role:
        type: enum
        labels:
          - anonymous
          - member
          - admin
          - super

      jwt:
        type: composite
        attributes:
          role:
            type: public.auth_role
          exp:
            type: bigint
          person_id:
            type: uuid
        dependsOn:
          - type public.auth_role

    functions:
      set_updated_at():
        returns: trigger
        language: plpgsql
        security: definer
        volatile: true
        body: |
          BEGIN
            NEW.updated_at = NOW();
            RETURN NEW;
          END;

      login(email citext, password text):
        returns: public.jwt
        language: plpgsql
        security: definer
        strict: true
        volatile: true
        dependsOn:
          - schema private
          - table private.account
          - function setting.get(text, integer)
        set:
          search_path: 'private, public, setting'
        body: |
          DECLARE
            account_record private.account%ROWTYPE;
            password_hash text;
          BEGIN
            SELECT a.*
              INTO account_record
            FROM private.account a
            JOIN private.email e
              ON e.person_id = a.person_id
             AND e.is_primary
            WHERE e.email = login.email;

            IF NOT FOUND THEN
              RAISE EXCEPTION 'Invalid email or password'
                USING ERRCODE = '28P01';
            END IF;

            password_hash := crypt(login.password, account_record.password_hash);

            IF password_hash <> account_record.password_hash THEN
              RAISE EXCEPTION 'Invalid email or password'
                USING ERRCODE = '28P01';
            END IF;

            RETURN (
              'anonymous',
              EXTRACT(EPOCH FROM now())::integer + setting.get_integer('jwt.ttl', 3600),
              account_record.person_id
            )::public.jwt;
          END;

  app:
    tables:
      person:
        columns:
          id:
            type: uuid
            primaryKey: true
            default: uuid_generate_v4()
          display_name:
            type: text
            notNull: true
          created_at:
            type: timestamptz
            default: NOW()
            notNull: true
          updated_at:
            type: timestamptz
            default: NOW()
            notNull: true

        primaryKey:
          person_pkey:
            columns: [id]

        indexes:
          person_display_name_idx:
            columns: [display_name]

        triggers:
          set_updated_at:
            timing: before
            events: [update]
            level: row
            procedure: public.set_updated_at()

        dependsOn:
          - function public.set_updated_at

  private:
    tables:
      account:
        columns:
          person_id:
            type: uuid
            primaryKey: true
          username:
            type: citext
            unique: true
          password_hash:
            type: text
            notNull: true

        foreignKeys:
          account_person_id_fkey:
            columns: [person_id]
            references:
              table: app.person
              columns: [id]
            onDelete: cascade

        dependsOn:
          - extension citext
          - table app.person
```

## Validation Rules

### Required Fields
Fields marked as `notNull: true` must have values.

### Type Validation
All values are validated against their specified types. Invalid types will cause validation errors.

### Constraint Validation
- Primary key constraints ensure uniqueness of primary key columns
- Unique constraints ensure column values are unique  
- Foreign key constraints validate referential integrity
- Check constraints ensure data satisfies a boolean expression
- Exclude constraints guarantee that rows do not overlap based on a specified index method
- Indexes can be defined for performance optimization

## Schema Inheritance and Dependencies

Schemas can reference other database objects using the `dependsOn` property. Dependencies include:
- Tables: `table <schema>.<table>`
- Extensions: `extension <extension_name>` 
- Types: `type <schema>.<type>`
- Functions: `function <schema>.<function>(args)`
- Schemas: `schema <schema_name>`

## Best Practices

1. **Use descriptive names** for schemas, tables, columns, and functions
2. **Document all objects** with clear descriptions  
3. **Set appropriate constraints** (primary keys, foreign keys, unique, not null)
4. **Manage dependencies explicitly** using `dependsOn`
5. **Version your schemas** to maintain backward compatibility
6. **Use extensions** for PostgreSQL functionality like `pgcrypto` and `citext`
7. **Organize objects into logical schemas** to avoid naming conflicts
8. **Define composite types** for complex return values from functions

## Error Handling

When validation fails, the system provides detailed error messages indicating:
- Which object failed validation
- Why it failed (type mismatch, constraint violation, etc.)
- Expected vs provided values where applicable
```