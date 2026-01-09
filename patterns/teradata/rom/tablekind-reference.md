# Teradata TableKind Reference

Complete list of `TableKind` values from `DBC.TablesV` system view.

## TableKind Values (Complete List)

### Selectable Tables (Data Storage)
| Code | Name | Selectable? | Description |
|------|------|-------------|-------------|
| `'T'` | **Table** | ✅ YES | Regular table with primary index |
| `'O'` | **No Primary Index Table** | ✅ YES | Table without primary index (heap table) |
| `'V'` | **View** | ✅ YES | Virtual table (view definition) |
| `'E'` | **External Table** | ✅ YES | Foreign table (references external data sources) |
| `'B'` | **Combined Table** | ✅ YES | Table with both row and column partitioning |

### Indexes (NOT Selectable)
| Code | Name | Selectable? | Description |
|------|------|-------------|-------------|
| `'I'` | **Join Index** | ❌ NO | Pre-aggregated structure, NOT directly selectable |
| `'H'` | **Hash Index** | ❌ NO | Hash index structure for performance |
| `'S'` | **Spatial Index** | ❌ NO | Geospatial index structure |

### Code Objects (NOT Selectable)
| Code | Name | Selectable? | Description |
|------|------|-------------|-------------|
| `'M'` | **Macro** | ❌ NO | Stored procedure/macro definition |
| `'P'` | **Stored Procedure** | ❌ NO | Stored procedure definition (SPL/SQL) |
| `'F'` | **Scalar Function** | ❌ NO | User-defined scalar function |
| `'R'` | **Table Function** | ❌ NO | User-defined table-returning function |
| `'A'` | **Aggregate** | ❌ NO | Aggregate function definition (UDF) |
| `'G'` | **Trigger** | ❌ NO | Database trigger definition |
| `'C'` | **Check Constraint** | ❌ NO | Table constraint definition |

### Temporary Objects (Session-Specific)
| Code | Name | Selectable? | Description |
|------|------|-------------|-------------|
| `'N'` | **Temporary Table** | ⚠️ SESSION | Volatile/Global temp table (session-scoped) |
| `'Q'` | **Queue Table** | ⚠️ LIMITED | Message queue table (special access patterns) |

### Data Types & Security
| Code | Name | Selectable? | Description |
|------|------|-------------|-------------|
| `'D'` | **Data Type** | ❌ NO | User-defined data type (UDT) |
| `'U'` | **User** | ❌ NO | Database user object |
| `'K'` | **Authorization** | ❌ NO | Authorization key/credential object |
| `'J'` | **Journal** | ❌ NO | Permanent journal table |

### Special Objects
| Code | Name | Selectable? | Description |
|------|------|-------------|-------------|
| `'X'` | **JAR** | ❌ NO | Java JAR file for UDFs |
| `'Y'` | **GLOP Set** | ❌ NO | Generic Large OBject set |
| `'Z'` | **Foreign Server** | ❌ NO | External data source connection |
| `'W'` | **Method** | ❌ NO | UDT method definition |
| `'L'` | **Error Table** | ✅ YES | Error logging table (selectable) |

## For nPath Analysis (Stage 2 Filter)

### ✅ RECOMMENDED: Regular Data Tables Only
```sql
WHERE TableKind IN ('T', 'O')
```
- **'T'** = Regular tables (most common, safest choice)
- **'O'** = No Primary Index tables (valid data tables)

### ⚠️ CONDITIONAL: Extended Selectable Objects
```sql
WHERE TableKind IN ('T', 'O', 'E', 'B', 'L')
```
- **'E'** = External tables (if accessing foreign data sources)
- **'B'** = Combined tables (row+column partitioning)
- **'L'** = Error tables (logging tables, less common)

**Note**: Views (`'V'`) are selectable but often contain derived/aggregated data which may not suit nPath sequential analysis.

### ❌ EXCLUDE: Non-Selectable Objects
All of these appear in `DBC.TablesV` but cannot be queried with `SELECT * FROM`:

**Indexes** (NOT queryable):
- **'I'** = Join indexes ← **Primary culprit for connectivity failures!**
- **'H'** = Hash indexes
- **'S'** = Spatial indexes

**Code Objects** (NOT data):
- **'M'** = Macros
- **'P'** = Stored procedures
- **'F'** = Scalar functions
- **'R'** = Table functions
- **'A'** = Aggregate functions
- **'G'** = Triggers
- **'C'** = Check constraints
- **'W'** = Methods

**Security & Metadata** (NOT tables):
- **'K'** = Authorization keys/credentials ← You mentioned this!
- **'U'** = User objects
- **'Z'** = Foreign server definitions ← Connection configs, not data!
- **'D'** = Data type definitions

**Special Objects** (NOT standard data):
- **'J'** = Journal tables (write-ahead logs)
- **'X'** = JAR files
- **'Y'** = GLOP sets
- **'N'** = Temporary tables (session-scoped)
- **'Q'** = Queue tables (special access patterns)

## Recommended Stage 2 Query Pattern

For nPath candidate discovery:

```sql
-- BASIC: Only regular and NoPI tables
SELECT DatabaseName, TableName, TableKind, RowCount, CreateTimeStamp
FROM DBC.TablesV
WHERE DatabaseName = '[database_name]'
  AND TableKind IN ('T', 'O')  -- Regular tables only
  AND RowCount > 1000          -- Sufficient data for analysis
ORDER BY RowCount DESC;

-- EXTENDED: Include external tables if needed
SELECT DatabaseName, TableName, TableKind, RowCount, CreateTimeStamp
FROM DBC.TablesV
WHERE DatabaseName = '[database_name]'
  AND TableKind IN ('T', 'O', 'E')  -- Regular + NoPI + External
  AND RowCount > 1000
ORDER BY RowCount DESC;
```

## Join Index Details (Why They Fail)

### What is a Join Index (`'I'`)?

A **join index** is a pre-computed, materialized structure that:
- Stores pre-joined or pre-aggregated data
- Automatically maintained by Teradata
- Used by the optimizer to speed up queries
- **NOT directly queryable** with standard SQL

### Example:
```sql
-- This creates a join index (TableKind = 'I')
CREATE JOIN INDEX sales_by_region AS
SELECT region_id, SUM(amount) as total_sales
FROM sales
GROUP BY region_id;

-- This FAILS (join index not directly selectable):
SELECT * FROM sales_by_region;  -- ❌ ERROR!

-- This WORKS (Teradata uses it automatically):
SELECT region_id, SUM(amount)
FROM sales
GROUP BY region_id;  -- ✓ Optimizer uses join index internally
```

### Why Stage 2 Was Finding Join Indexes:

```sql
-- OLD Stage 2 query (no TableKind filter):
SELECT DatabaseName, TableName
FROM DBC.TablesV
WHERE DatabaseName = 'data_scientist'
  AND TableName LIKE '%event%';
-- Returns: telco_events (TableKind = 'I') ← Join index!

-- NEW Stage 2 query (with TableKind filter):
SELECT DatabaseName, TableName, TableKind
FROM DBC.TablesV
WHERE DatabaseName = 'data_scientist'
  AND TableName LIKE '%event%'
  AND TableKind IN ('T', 'O');  -- ← Excludes join indexes!
-- Returns: Only actual tables
```

## Verification Queries

### Check TableKind of a specific table:
```sql
SELECT DatabaseName, TableName, TableKind, 
       CASE TableKind
         WHEN 'T' THEN 'Regular Table'
         WHEN 'O' THEN 'NoPI Table'
         WHEN 'I' THEN 'Join Index (NOT SELECTABLE!)'
         WHEN 'V' THEN 'View'
         ELSE 'Other: ' || TableKind
       END AS TableType
FROM DBC.TablesV
WHERE DatabaseName = 'data_scientist'
  AND TableName = 'telco_events';
```

### Find all join indexes in a database:
```sql
SELECT DatabaseName, TableName, CreateTimeStamp
FROM DBC.TablesV
WHERE DatabaseName = 'data_scientist'
  AND TableKind = 'I'
ORDER BY TableName;
```

### Find selectable tables with event data:
```sql
SELECT DatabaseName, TableName, TableKind, RowCount
FROM DBC.TablesV
WHERE DatabaseName = 'data_scientist'
  AND TableKind IN ('T', 'O')  -- Only selectable tables
  AND (TableName LIKE '%event%' 
    OR TableName LIKE '%transaction%'
    OR TableName LIKE '%activity%')
  AND RowCount > 1000
ORDER BY RowCount DESC;
```

## Summary

For **Stage 2 nPath candidate discovery**, use:

```sql
WHERE TableKind IN ('T', 'O')  -- Safe, selectable tables only
```

This prevents recommending:
- Join indexes (`'I'`) that cause connectivity failures
- Views (`'V'`) that may have complex logic
- Code objects that aren't tables
- Temporary tables that may not persist

---

**Key Takeaway**: `TableKind = 'I'` (join indexes) appear in `DBC.TablesV` but **cannot be selected from directly**, which was causing all the Stage 3 connectivity failures!
