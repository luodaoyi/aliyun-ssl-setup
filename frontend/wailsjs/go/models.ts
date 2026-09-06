export namespace aliyun {
	
	export class Progress {
	    stage: string;
	    title: string;
	    detail: string;
	    done: number;
	    total: number;
	    percent: number;
	    level: string;
	
	    static createFrom(source: any = {}) {
	        return new Progress(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.stage = source["stage"];
	        this.title = source["title"];
	        this.detail = source["detail"];
	        this.done = source["done"];
	        this.total = source["total"];
	        this.percent = source["percent"];
	        this.level = source["level"];
	    }
	}

}

export namespace deploy {
	
	export class ListenerInfo {
	    load_balancer_id: string;
	    load_balancer_name: string;
	    port: number;
	    cert_id: string;
	    cert_name: string;
	    region: string;
	    address: string;
	
	    static createFrom(source: any = {}) {
	        return new ListenerInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.load_balancer_id = source["load_balancer_id"];
	        this.load_balancer_name = source["load_balancer_name"];
	        this.port = source["port"];
	        this.cert_id = source["cert_id"];
	        this.cert_name = source["cert_name"];
	        this.region = source["region"];
	        this.address = source["address"];
	    }
	}

}

export namespace main {
	
	export class AppConfig {
	    access_key_id: string;
	    access_key_secret: string;
	    regions: string[];
	    acme_email: string;
	    acme_dir_url: string;
	    eab_kid: string;
	    eab_hmac: string;
	    key_type: string;
	    smtp_host: string;
	    smtp_port: number;
	    smtp_user: string;
	    smtp_pass: string;
	    mail_to: string;
	
	    static createFrom(source: any = {}) {
	        return new AppConfig(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.access_key_id = source["access_key_id"];
	        this.access_key_secret = source["access_key_secret"];
	        this.regions = source["regions"];
	        this.acme_email = source["acme_email"];
	        this.acme_dir_url = source["acme_dir_url"];
	        this.eab_kid = source["eab_kid"];
	        this.eab_hmac = source["eab_hmac"];
	        this.key_type = source["key_type"];
	        this.smtp_host = source["smtp_host"];
	        this.smtp_port = source["smtp_port"];
	        this.smtp_user = source["smtp_user"];
	        this.smtp_pass = source["smtp_pass"];
	        this.mail_to = source["mail_to"];
	    }
	}
	export class DeployRequest {
	    cert_key: string;
	    targets: string[];
	    domain: string;
	    region: string;
	    lb_id: string;
	    port: number;
	
	    static createFrom(source: any = {}) {
	        return new DeployRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.cert_key = source["cert_key"];
	        this.targets = source["targets"];
	        this.domain = source["domain"];
	        this.region = source["region"];
	        this.lb_id = source["lb_id"];
	        this.port = source["port"];
	    }
	}
	export class TaskLog {
	    time: string;
	    level: string;
	    msg: string;
	
	    static createFrom(source: any = {}) {
	        return new TaskLog(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = source["time"];
	        this.level = source["level"];
	        this.msg = source["msg"];
	    }
	}

}

export namespace store {
	
	export class CertEntry {
	    source: string;
	    name: string;
	    domains?: string[];
	    issuer?: string;
	    not_before?: string;
	    not_after?: string;
	    days?: number;
	    region?: string;
	    id?: string;
	    bucket?: string;
	    note?: string;
	
	    static createFrom(source: any = {}) {
	        return new CertEntry(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.source = source["source"];
	        this.name = source["name"];
	        this.domains = source["domains"];
	        this.issuer = source["issuer"];
	        this.not_before = source["not_before"];
	        this.not_after = source["not_after"];
	        this.days = source["days"];
	        this.region = source["region"];
	        this.id = source["id"];
	        this.bucket = source["bucket"];
	        this.note = source["note"];
	    }
	}
	export class IssuedCert {
	    key: string;
	    domains: string[];
	    cert_path: string;
	    key_path: string;
	    not_after: string;
	    days: number;
	    issuer: string;
	    ca: string;
	    cert_id?: string;
	    created_at: string;
	
	    static createFrom(source: any = {}) {
	        return new IssuedCert(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.key = source["key"];
	        this.domains = source["domains"];
	        this.cert_path = source["cert_path"];
	        this.key_path = source["key_path"];
	        this.not_after = source["not_after"];
	        this.days = source["days"];
	        this.issuer = source["issuer"];
	        this.ca = source["ca"];
	        this.cert_id = source["cert_id"];
	        this.created_at = source["created_at"];
	    }
	}
	export class ScanResult {
	    // Go type: time
	    time: any;
	    certs: CertEntry[];
	    errors?: string[];
	
	    static createFrom(source: any = {}) {
	        return new ScanResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.time = this.convertValues(source["time"], null);
	        this.certs = this.convertValues(source["certs"], CertEntry);
	        this.errors = source["errors"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}

}

