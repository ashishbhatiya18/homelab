terraform {
  required_version = ">= 1.9"

  # Remote state via HCP Terraform (free tier).
  # Create a workspace at https://app.terraform.io, then:
  #   1. Set organization and workspace below.
  #   2. Add TF_CLOUD_TOKEN to GitHub Actions secrets.
  #   3. Run `terraform login` locally before `terraform init`.
  cloud {
    organization = "ashishbhatiya18-homelab"
    workspaces {
      name = "localstack-cloudflare"
    }
  }

  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5"
    }
  }
}

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}
