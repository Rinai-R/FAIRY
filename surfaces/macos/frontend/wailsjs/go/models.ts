export namespace coreclient {
	
	export class VisualState {
	    id: string;
	    description: string;
	    imagePath: string;
	
	    static createFrom(source: any = {}) {
	        return new VisualState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.description = source["description"];
	        this.imagePath = source["imagePath"];
	    }
	}
	export class VisualManifest {
	    packId: string;
	    states: VisualState[];
	
	    static createFrom(source: any = {}) {
	        return new VisualManifest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.packId = source["packId"];
	        this.states = this.convertValues(source["states"], VisualState);
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
	export class CharacterAppearance {
	    status: string;
	    visual?: VisualManifest;
	
	    static createFrom(source: any = {}) {
	        return new CharacterAppearance(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.status = source["status"];
	        this.visual = this.convertValues(source["visual"], VisualManifest);
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
	export class CharacterRecord {
	    characterId: string;
	    revision: number;
	    name: string;
	    appearance: CharacterAppearance;
	
	    static createFrom(source: any = {}) {
	        return new CharacterRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.characterId = source["characterId"];
	        this.revision = source["revision"];
	        this.name = source["name"];
	        this.appearance = this.convertValues(source["appearance"], CharacterAppearance);
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
	export class MessageRecord {
	    id: string;
	    conversationId: string;
	    turnId: string;
	    sequence: number;
	    role: string;
	    content: string;
	    createdAtUnixMs: number;
	
	    static createFrom(source: any = {}) {
	        return new MessageRecord(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.conversationId = source["conversationId"];
	        this.turnId = source["turnId"];
	        this.sequence = source["sequence"];
	        this.role = source["role"];
	        this.content = source["content"];
	        this.createdAtUnixMs = source["createdAtUnixMs"];
	    }
	}
	export class OpenSessionResponse {
	    conversationId: string;
	    characterId: string;
	    messageCount: number;
	    endpoint: string;
	
	    static createFrom(source: any = {}) {
	        return new OpenSessionResponse(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.conversationId = source["conversationId"];
	        this.characterId = source["characterId"];
	        this.messageCount = source["messageCount"];
	        this.endpoint = source["endpoint"];
	    }
	}
	

}

export namespace main {
	
	export class ConnectionState {
	    endpoint: string;
	    endpointKey: string;
	    hasToken: boolean;
	
	    static createFrom(source: any = {}) {
	        return new ConnectionState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.endpoint = source["endpoint"];
	        this.endpointKey = source["endpointKey"];
	        this.hasToken = source["hasToken"];
	    }
	}
	export class VisualAsset {
	    id: string;
	    description: string;
	    dataUrl: string;
	
	    static createFrom(source: any = {}) {
	        return new VisualAsset(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.description = source["description"];
	        this.dataUrl = source["dataUrl"];
	    }
	}
	export class SessionState {
	    connection: ConnectionState;
	    session: coreclient.OpenSessionResponse;
	    messages: coreclient.MessageRecord[];
	    character: coreclient.CharacterRecord;
	    visuals: VisualAsset[];
	
	    static createFrom(source: any = {}) {
	        return new SessionState(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.connection = this.convertValues(source["connection"], ConnectionState);
	        this.session = this.convertValues(source["session"], coreclient.OpenSessionResponse);
	        this.messages = this.convertValues(source["messages"], coreclient.MessageRecord);
	        this.character = this.convertValues(source["character"], coreclient.CharacterRecord);
	        this.visuals = this.convertValues(source["visuals"], VisualAsset);
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

