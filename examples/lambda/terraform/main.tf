# terraform-lite alternative to SAM — Function URL + IAM auth.
# Usage:
#   cd examples/lambda/terraform
#   terraform init && terraform apply
#   terraform output function_url  → export LAMBDA_FUNCTION_URL=...

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
    archive = {
      source  = "hashicorp/archive"
      version = ">= 2.4"
    }
  }
}

provider "aws" {
  region = var.region
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "function_name" {
  type    = string
  default = "kaptanto-cdc-orders"
}

data "archive_file" "handler" {
  type        = "zip"
  source_dir  = "${path.module}/../src"
  output_path = "${path.module}/handler.zip"
}

resource "aws_iam_role" "lambda" {
  name = "${var.function_name}-role"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Action    = "sts:AssumeRole"
      Effect    = "Allow"
      Principal = { Service = "lambda.amazonaws.com" }
    }]
  })
}

resource "aws_iam_role_policy_attachment" "basic" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

resource "aws_lambda_function" "cdc" {
  function_name    = var.function_name
  role             = aws_iam_role.lambda.arn
  handler          = "handler.handler"
  runtime          = "python3.12"
  filename         = data.archive_file.handler.output_path
  source_code_hash = data.archive_file.handler.output_base64sha256
  timeout          = 10
  memory_size      = 128
}

resource "aws_lambda_function_url" "cdc" {
  function_name      = aws_lambda_function.cdc.function_name
  authorization_type = "AWS_IAM"
  invoke_mode        = "BUFFERED"
}

resource "aws_lambda_permission" "url" {
  statement_id           = "FunctionURLAllowInvoke"
  action                 = "lambda:InvokeFunctionUrl"
  function_name          = aws_lambda_function.cdc.function_name
  principal              = "*"
  function_url_auth_type = "AWS_IAM"
}

output "function_url" {
  value = aws_lambda_function_url.cdc.function_url
}

output "region" {
  value = var.region
}
