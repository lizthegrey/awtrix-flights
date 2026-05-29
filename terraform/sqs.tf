# SQS fan-out queue. EventBridge fires the publisher Lambda once a minute;
# the fanout branch enqueues 4 messages with DelaySeconds 0/15/30/45, and
# the same Lambda then consumes each delayed message via the event source
# mapping below, giving ~15 s effective scan cadence.

resource "aws_sqs_queue" "publisher_fanout" {
  name = "${var.name_prefix}-publisher-fanout"
  # Retention is short on purpose — anything not picked up in ~70 s is
  # stale anyway (the next EventBridge tick is going to enqueue fresh).
  message_retention_seconds = 70
  # Visibility timeout >= Lambda timeout so messages aren't re-delivered
  # mid-execution after a slow adsb.fi response.
  visibility_timeout_seconds = 30
  tags                       = local.default_tags
}

resource "aws_lambda_event_source_mapping" "publisher_fanout" {
  event_source_arn = aws_sqs_queue.publisher_fanout.arn
  function_name    = aws_lambda_function.publisher.arn
  batch_size       = 1 # one tick per message; no batching
  enabled          = true
}
