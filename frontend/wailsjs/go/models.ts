export namespace bilibili {
	
	export class EpisodeInfo {
	    index: number;
	    cid: number;
	    bvid: string;
	    aid: number;
	    epid: number;
	    title: string;
	    longTitle: string;
	    duration: number;
	    durationStr: string;
	    cover: string;
	    badge: string;
	
	    static createFrom(source: any = {}) {
	        return new EpisodeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.index = source["index"];
	        this.cid = source["cid"];
	        this.bvid = source["bvid"];
	        this.aid = source["aid"];
	        this.epid = source["epid"];
	        this.title = source["title"];
	        this.longTitle = source["longTitle"];
	        this.duration = source["duration"];
	        this.durationStr = source["durationStr"];
	        this.cover = source["cover"];
	        this.badge = source["badge"];
	    }
	}
	export class QRCodeInfo {
	    url: string;
	    qrcodeKey: string;
	
	    static createFrom(source: any = {}) {
	        return new QRCodeInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.url = source["url"];
	        this.qrcodeKey = source["qrcodeKey"];
	    }
	}
	export class QRStatus {
	    code: number;
	    message: string;
	    isSuccess: boolean;
	    isExpired: boolean;
	
	    static createFrom(source: any = {}) {
	        return new QRStatus(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.code = source["code"];
	        this.message = source["message"];
	        this.isSuccess = source["isSuccess"];
	        this.isExpired = source["isExpired"];
	    }
	}
	export class QualityOption {
	    id: number;
	    label: string;
	    codecs: string;
	    isVipRequired: boolean;
	    isLoginRequired: boolean;
	    isAvailable: boolean;
	
	    static createFrom(source: any = {}) {
	        return new QualityOption(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.label = source["label"];
	        this.codecs = source["codecs"];
	        this.isVipRequired = source["isVipRequired"];
	        this.isLoginRequired = source["isLoginRequired"];
	        this.isAvailable = source["isAvailable"];
	    }
	}
	export class UserInfo {
	    isLogin: boolean;
	    mid: number;
	    uname: string;
	    face: string;
	    level: number;
	    vipType: number;
	    vipStatus: number;
	    vipLabel: string;
	    vipDueDate: number;
	    vipDueStr: string;
	    money: number;
	
	    static createFrom(source: any = {}) {
	        return new UserInfo(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.isLogin = source["isLogin"];
	        this.mid = source["mid"];
	        this.uname = source["uname"];
	        this.face = source["face"];
	        this.level = source["level"];
	        this.vipType = source["vipType"];
	        this.vipStatus = source["vipStatus"];
	        this.vipLabel = source["vipLabel"];
	        this.vipDueDate = source["vipDueDate"];
	        this.vipDueStr = source["vipDueStr"];
	        this.money = source["money"];
	    }
	}
	export class VideoDetail {
	    type: string;
	    bvid: string;
	    aid: number;
	    title: string;
	    cover: string;
	    description: string;
	    duration: number;
	    durationStr: string;
	    pubDate: number;
	    ownerName: string;
	    ownerFace: string;
	    ownerMid: number;
	    viewCount: number;
	    likeCount: number;
	    danmakuCount: number;
	    isCollection: boolean;
	    totalParts: number;
	    episodes: EpisodeInfo[];
	    defaultPage: number;
	
	    static createFrom(source: any = {}) {
	        return new VideoDetail(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.type = source["type"];
	        this.bvid = source["bvid"];
	        this.aid = source["aid"];
	        this.title = source["title"];
	        this.cover = source["cover"];
	        this.description = source["description"];
	        this.duration = source["duration"];
	        this.durationStr = source["durationStr"];
	        this.pubDate = source["pubDate"];
	        this.ownerName = source["ownerName"];
	        this.ownerFace = source["ownerFace"];
	        this.ownerMid = source["ownerMid"];
	        this.viewCount = source["viewCount"];
	        this.likeCount = source["likeCount"];
	        this.danmakuCount = source["danmakuCount"];
	        this.isCollection = source["isCollection"];
	        this.totalParts = source["totalParts"];
	        this.episodes = this.convertValues(source["episodes"], EpisodeInfo);
	        this.defaultPage = source["defaultPage"];
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

export namespace config {
	
	export class Settings {
	    downloadDir: string;
	    defaultQuality: string;
	    defaultCodec: string;
	    maxConcurrent: number;
	    threadsPerTask: number;
	    autoMerge: boolean;
	    deleteTempFiles: boolean;
	    ffmpegPath: string;
	    autoClipboard: boolean;
	    fileNameTemplate: string;
	    theme: string;
	
	    static createFrom(source: any = {}) {
	        return new Settings(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.downloadDir = source["downloadDir"];
	        this.defaultQuality = source["defaultQuality"];
	        this.defaultCodec = source["defaultCodec"];
	        this.maxConcurrent = source["maxConcurrent"];
	        this.threadsPerTask = source["threadsPerTask"];
	        this.autoMerge = source["autoMerge"];
	        this.deleteTempFiles = source["deleteTempFiles"];
	        this.ffmpegPath = source["ffmpegPath"];
	        this.autoClipboard = source["autoClipboard"];
	        this.fileNameTemplate = source["fileNameTemplate"];
	        this.theme = source["theme"];
	    }
	}

}

export namespace downloader {
	
	export class DownloadRequest {
	    bvid: string;
	    aid: number;
	    title: string;
	    cover: string;
	    isBangumi: boolean;
	    targetQuality: string;
	    targetCodec: string;
	    episodes: number[];
	
	    static createFrom(source: any = {}) {
	        return new DownloadRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.bvid = source["bvid"];
	        this.aid = source["aid"];
	        this.title = source["title"];
	        this.cover = source["cover"];
	        this.isBangumi = source["isBangumi"];
	        this.targetQuality = source["targetQuality"];
	        this.targetCodec = source["targetCodec"];
	        this.episodes = source["episodes"];
	    }
	}
	export class DownloadTask {
	    id: string;
	    bvid: string;
	    aid: number;
	    cid: number;
	    epid: number;
	    isBangumi: boolean;
	    title: string;
	    partTitle: string;
	    cover: string;
	    targetQuality: string;
	    targetCodec: string;
	    qualityId: number;
	    qualityLabel: string;
	    codec: string;
	    status: string;
	    progress: number;
	    speed: number;
	    speedStr: string;
	    downloadedBytes: number;
	    totalBytes: number;
	    sizeStr: string;
	    etaStr: string;
	    duration: number;
	    errorMsg: string;
	    createdAt: number;
	    completedAt: number;
	    outputPath: string;
	    videoTmpPath: string;
	    audioTmpPath: string;
	
	    static createFrom(source: any = {}) {
	        return new DownloadTask(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.bvid = source["bvid"];
	        this.aid = source["aid"];
	        this.cid = source["cid"];
	        this.epid = source["epid"];
	        this.isBangumi = source["isBangumi"];
	        this.title = source["title"];
	        this.partTitle = source["partTitle"];
	        this.cover = source["cover"];
	        this.targetQuality = source["targetQuality"];
	        this.targetCodec = source["targetCodec"];
	        this.qualityId = source["qualityId"];
	        this.qualityLabel = source["qualityLabel"];
	        this.codec = source["codec"];
	        this.status = source["status"];
	        this.progress = source["progress"];
	        this.speed = source["speed"];
	        this.speedStr = source["speedStr"];
	        this.downloadedBytes = source["downloadedBytes"];
	        this.totalBytes = source["totalBytes"];
	        this.sizeStr = source["sizeStr"];
	        this.etaStr = source["etaStr"];
	        this.duration = source["duration"];
	        this.errorMsg = source["errorMsg"];
	        this.createdAt = source["createdAt"];
	        this.completedAt = source["completedAt"];
	        this.outputPath = source["outputPath"];
	        this.videoTmpPath = source["videoTmpPath"];
	        this.audioTmpPath = source["audioTmpPath"];
	    }
	}

}

