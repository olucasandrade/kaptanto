import type {
  ICredentialType,
  INodeProperties,
} from "n8n-workflow";

export class KaptantoApi implements ICredentialType {
  name = "kaptantoApi";
  displayName = "Kaptanto API";
  documentationUrl = "https://github.com/olucasandrade/kaptanto";

  properties: INodeProperties[] = [
    {
      displayName: "Base URL",
      name: "baseUrl",
      type: "string",
      default: "http://localhost:7654",
      placeholder: "https://kaptanto.example.com",
      description: "Base URL of the Kaptanto instance (SSE endpoint is at /events)",
    },
    {
      displayName: "Auth Token",
      name: "authToken",
      type: "string",
      typeOptions: { password: true },
      default: "",
      description: "Bearer token for authenticating with Kaptanto (optional if insecure mode is enabled)",
    },
  ];
}
