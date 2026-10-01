variable "realm" {
  type    = string
  default = "jungle"
}

variable "audience" {
  description = "Audience every access token must carry (validated by the API)."
  type        = string
  default     = "jungle-api"
}

variable "game_providers" {
  description = "Game provider ids; each gets a client whose id equals the provider id."
  type        = list(string)
}

variable "internal_client" {
  type    = string
  default = "jungle-internal"
}

variable "shortlived_client" {
  type    = string
  default = "provider-c-shortlived"
}

variable "shortlived_provider_id" {
  type    = string
  default = "provider-c"
}

variable "shortlived_lifespan_seconds" {
  type    = string
  default = "5"
}
