#############################
# FRONT DOOR (Gateway API)  #
#############################

# One stable Gateway in default; AWS LBC creates exactly one ALB and reuses it.
resource "kubernetes_manifest" "gateway" {
  manifest = {
    apiVersion = "gateway.networking.k8s.io/v1"
    kind       = "Gateway"
    metadata = {
      name      = "app-gateway"
      namespace = "default"
      annotations = {
        "alb.ingress.kubernetes.io/scheme"                  = "internet-facing"
        "alb.ingress.kubernetes.io/target-type"             = "ip"
        "alb.ingress.kubernetes.io/healthcheck-path"        = "/"
        # add any other controller annotations you previously used on Ingress
      }
    }
    spec = {
      gatewayClassName = "alb"
      listeners = [
        {
          name     = "http"
          protocol = "HTTP"
          port     = 80
          hostname = var.host
        }
        # If you need HTTPS, add a second listener with protocol HTTPS and your ACM cert annotation at Gateway level.
      ]
    }
  }
}

# Route living in default that points to Services in blue/green namespaces.
resource "kubernetes_manifest" "http_route" {
  manifest = {
    apiVersion = "gateway.networking.k8s.io/v1"
    kind       = "HTTPRoute"
    metadata = {
      name      = "app-route"
      namespace = "default"
    }
    spec = {
      parentRefs = [{
        name      = kubernetes_manifest.gateway.manifest.metadata.name
        namespace = kubernetes_manifest.gateway.manifest.metadata.namespace
      }]
      hostnames = [var.host]
      rules = [{
        backendRefs = [
          {
            name      = "frontendservice-blue-service"
            namespace = "microservicesdemo-blue"
            port      = 80
            weight    = var.traffic_split.blue
          },
          {
            name      = "frontendservice-green-service"
            namespace = "microservicesdemo-green"
            port      = 80
            weight    = var.traffic_split.green
          }
        ]
      }]
    }
  }
  depends_on = [kubernetes_manifest.gateway]
}

##########################################
# CROSS-NAMESPACE PERMISSIONS (required) #
##########################################

resource "kubernetes_manifest" "refpolicy_blue" {
  manifest = {
    apiVersion = "gateway.networking.k8s.io/v1"
    kind       = "ReferencePolicy"
    metadata = {
      name      = "allow-default"
      namespace = "microservicesdemo-blue"
    }
    spec = {
      from = [{
        group     = "gateway.networking.k8s.io"
        kind      = "HTTPRoute"
        namespace = "default"
      }]
      to = [{ group = "", kind = "Service" }]
    }
  }
}

resource "kubernetes_manifest" "refpolicy_green" {
  manifest = {
    apiVersion = "gateway.networking.k8s.io/v1"
    kind       = "ReferencePolicy"
    metadata = {
      name      = "allow-default"
      namespace = "microservicesdemo-green"
    }
    spec = {
      from = [{
        group     = "gateway.networking.k8s.io"
        kind      = "HTTPRoute"
        namespace = "default"
      }]
      to = [{ group = "", kind = "Service" }]
    }
  }
}

##################
# Helpful output #
##################

# The AWS LBC populates status with the ALB hostname. Use this for Route53.
output "gateway_addresses" {
  value       = try(kubernetes_manifest.gateway.manifest.status.addresses, null)
  description = "Gateway status addresses (includes ALB hostname once ready)"
}


helm upgrade --install aws-load-balancer-controller eks/aws-load-balancer-controller \
  -n kube-system \
  --set clusterName=<CLUSTER_NAME> \
  --set serviceAccount.create=false \
  --set serviceAccount.name=aws-load-balancer-controller \
  --set enableGatewayAPI=true


output "host" {
  value = var.host
}
