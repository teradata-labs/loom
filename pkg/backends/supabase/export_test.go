// Copyright © 2026 Teradata Corporation - All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package supabase

import loomv1 "github.com/teradata-labs/loom/gen/go/loom/v1"

// BuildConnectionStringForTest exposes buildConnectionString for testing.
func BuildConnectionStringForTest(config *loomv1.SupabaseConnection) string {
	return buildConnectionString(config)
}

// InternalSchemasForTest exposes internalSchemas for testing.
func InternalSchemasForTest() []string {
	result := make([]string, len(internalSchemas))
	copy(result, internalSchemas)
	return result
}
