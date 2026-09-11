// Copyright 2026 Teradata
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package task

import "errors"

// ErrTimelineTaskIDRequired is returned when a timeline read is attempted
// without a task ID. An unscoped read would scan every source in full.
var ErrTimelineTaskIDRequired = errors.New("task timeline: task_id is required")
