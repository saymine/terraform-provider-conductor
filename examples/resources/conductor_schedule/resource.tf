resource "conductor_schedule" "this" {
  manifest = <<EOF
  {
    "name": "daily_report_schedule",
    "description": "Runs the daily report every weekday at 09:00 UTC",
    "cronExpression": "0 0 9 * * MON-FRI",
    "zoneId": "UTC",
    "startWorkflowRequest": {
      "name": "daily_report_workflow",
      "input": {},
      "correlationId": "daily-report-${scheduledTime}"
    },
    "paused": false,
    "runCatchupScheduleInstances": false
  }
  EOF
}
