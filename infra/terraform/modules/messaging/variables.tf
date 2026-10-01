variable "game_providers" {
  description = "Game provider ids allowed to send to the ingress queue."
  type        = list(string)
}

variable "ingress_visibility_timeout_seconds" {
  type    = number
  default = 60
}

variable "ingress_max_receive_count" {
  description = "Deliveries before SQS moves a message to the ingress DLQ."
  type        = number
  default     = 5
}
